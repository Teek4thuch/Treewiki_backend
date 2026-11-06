package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func GrantRuntime(ctx context.Context, ownerURL, runtimeURL string) error {
	if ownerURL == "" || runtimeURL == "" {
		return errors.New("MIGRATION_DATABASE_URL and DATABASE_URL are required")
	}
	owner, err := pgx.ParseConfig(ownerURL)
	if err != nil {
		return errors.New("invalid MIGRATION_DATABASE_URL")
	}
	runtime, err := pgx.ParseConfig(runtimeURL)
	if err != nil {
		return errors.New("invalid DATABASE_URL")
	}
	if owner.User == runtime.User || runtime.User == "" || runtime.Password == "" || owner.Database != runtime.Database || owner.Host != runtime.Host || owner.Port != runtime.Port {
		return errors.New("provide distinct owner/runtime users on the same database endpoint")
	}
	conn, err := pgx.ConnectConfig(ctx, owner)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	var exists bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname=$1)", runtime.User).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		var statement string
		if err := tx.QueryRow(ctx, "SELECT format('CREATE ROLE %I LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS PASSWORD %L', $1::text, $2::text)", runtime.User, runtime.Password).Scan(&statement); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, statement); err != nil {
			return err
		}
	}
	role := pgx.Identifier{runtime.User}.Sanitize()
	database := pgx.Identifier{owner.Database}.Sanitize()
	for _, statement := range []string{
		"REVOKE CREATE ON SCHEMA public FROM PUBLIC",
		"REVOKE CREATE ON DATABASE " + database + " FROM PUBLIC",
		"REVOKE ALL ON catalog_names, catalog_taxa, catalog_releases FROM " + role,
		"REVOKE ALL ON enrichment_matches, enrichment_descriptions, enrichment_images, enrichment_progress FROM " + role,
		"REVOKE ALL ON SEQUENCE catalog_releases_id_seq FROM " + role,
		"GRANT CONNECT ON DATABASE " + database + " TO " + role,
		"GRANT USAGE ON SCHEMA public TO " + role,
		"GRANT SELECT ON catalog_names, catalog_taxa, catalog_releases TO " + role,
		"GRANT SELECT ON enrichment_matches, enrichment_descriptions, enrichment_images TO " + role,
	} {
		if _, err := tx.Exec(ctx, statement); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	pool, err := Open(ctx, runtimeURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	return ValidateRuntimeRole(ctx, pool)
}

func ValidateRuntimeRole(ctx context.Context, pool *pgxpool.Pool) error {
	var ready bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.catalog_releases') IS NOT NULL
AND to_regclass('public.catalog_taxa') IS NOT NULL
AND to_regclass('public.catalog_names') IS NOT NULL
AND to_regclass('public.enrichment_matches') IS NOT NULL
AND to_regclass('public.enrichment_descriptions') IS NOT NULL
AND to_regclass('public.enrichment_images') IS NOT NULL
AND to_regclass('public.enrichment_progress') IS NOT NULL`).Scan(&ready); err != nil {
		return err
	}
	if !ready {
		return fmt.Errorf("catalog schema is missing; run migrate up")
	}
	var unsafe, readable bool
	if err := pool.QueryRow(ctx, `SELECT r.rolsuper OR r.rolcreatedb OR r.rolcreaterole OR r.rolbypassrls
OR has_schema_privilege(current_user,'public','CREATE')
OR has_database_privilege(current_user,current_database(),'CREATE')
OR EXISTS (SELECT 1 FROM pg_auth_members m WHERE m.member=r.oid)
OR EXISTS (SELECT 1 FROM pg_class c WHERE c.relnamespace='public'::regnamespace AND pg_has_role(r.oid,c.relowner,'MEMBER'))
OR has_table_privilege(current_user,'public.catalog_releases','INSERT,UPDATE,DELETE,TRUNCATE')
OR has_table_privilege(current_user,'public.catalog_taxa','INSERT,UPDATE,DELETE,TRUNCATE')
OR has_table_privilege(current_user,'public.catalog_names','INSERT,UPDATE,DELETE,TRUNCATE')
OR has_table_privilege(current_user,'public.enrichment_matches','INSERT,UPDATE,DELETE,TRUNCATE')
OR has_table_privilege(current_user,'public.enrichment_descriptions','INSERT,UPDATE,DELETE,TRUNCATE')
OR has_table_privilege(current_user,'public.enrichment_images','INSERT,UPDATE,DELETE,TRUNCATE')
OR has_table_privilege(current_user,'public.enrichment_progress','INSERT,UPDATE,DELETE,TRUNCATE'),
has_table_privilege(current_user,'public.catalog_releases','SELECT')
AND has_table_privilege(current_user,'public.catalog_taxa','SELECT')
AND has_table_privilege(current_user,'public.catalog_names','SELECT')
AND has_table_privilege(current_user,'public.enrichment_matches','SELECT')
AND has_table_privilege(current_user,'public.enrichment_descriptions','SELECT')
AND has_table_privilege(current_user,'public.enrichment_images','SELECT')
FROM pg_roles r WHERE r.rolname=current_user`).Scan(&unsafe, &readable); err != nil {
		return err
	}
	if unsafe || !readable {
		return errors.New("DATABASE_URL must use a read-only catalog role; run database grant-runtime")
	}
	return nil
}
