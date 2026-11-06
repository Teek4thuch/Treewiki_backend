package enrichment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type api struct {
	client                       *http.Client
	userAgent                    string
	wikidata, commons, wikipedia string
	last                         time.Time
	interval                     time.Duration
}

func newAPI(userAgent string) *api {
	return &api{client: &http.Client{Timeout: 30 * time.Second}, userAgent: userAgent,
		wikidata: "https://www.wikidata.org/w/api.php", commons: "https://commons.wikimedia.org/w/api.php",
		wikipedia: "https://%s.wikipedia.org/w/api.php", interval: time.Second}
}

func (a *api) get(ctx context.Context, base string, params url.Values, dst any) error {
	params.Set("format", "json")
	params.Set("formatversion", "2")
	for attempt := 0; attempt < 3; attempt++ {
		if wait := time.Until(a.last.Add(a.interval)); wait > 0 {
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"?"+params.Encode(), nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", a.userAgent)
		a.last = time.Now()
		resp, err := a.client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if attempt == 2 {
				return err
			}
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode >= 500 {
			delay := time.Duration(attempt+1) * 2 * time.Second
			if seconds, e := strconv.Atoi(resp.Header.Get("Retry-After")); e == nil && seconds > 0 {
				delay = time.Duration(seconds) * time.Second
			}
			resp.Body.Close()
			if attempt == 2 {
				return fmt.Errorf("Wikimedia HTTP %d", resp.StatusCode)
			}
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return ctx.Err()
			}
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return fmt.Errorf("Wikimedia HTTP %d", resp.StatusCode)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if err != nil {
			return err
		}
		var envelope struct {
			Error *struct {
				Code string `json:"code"`
				Info string `json:"info"`
			} `json:"error"`
		}
		if err = json.Unmarshal(data, &envelope); err != nil {
			return err
		}
		if envelope.Error != nil {
			return fmt.Errorf("Wikimedia API %s: %s", envelope.Error.Code, envelope.Error.Info)
		}
		return json.Unmarshal(data, dst)
	}
	return errors.New("Wikimedia retry limit reached")
}

type claim struct {
	MainSnak struct {
		DataValue struct {
			Value json.RawMessage `json:"value"`
		} `json:"datavalue"`
	} `json:"mainsnak"`
	Rank string `json:"rank"`
}
type entity struct {
	ID        string             `json:"id"`
	Claims    map[string][]claim `json:"claims"`
	Sitelinks map[string]struct {
		Title string `json:"title"`
	} `json:"sitelinks"`
}

func values(e entity, property string) []json.RawMessage {
	var result []json.RawMessage
	for _, c := range e.Claims[property] {
		if c.Rank != "deprecated" && len(c.MainSnak.DataValue.Value) > 0 {
			result = append(result, c.MainSnak.DataValue.Value)
		}
	}
	return result
}
func stringsOf(e entity, property string) []string {
	var result []string
	for _, raw := range values(e, property) {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			result = append(result, s)
		}
	}
	return result
}
func idsOf(e entity, property string) []string {
	var result []string
	for _, raw := range values(e, property) {
		var v struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &v) == nil && v.ID != "" {
			result = append(result, v.ID)
		}
	}
	return result
}
func has(items []string, s string) bool {
	for _, item := range items {
		if item == s {
			return true
		}
	}
	return false
}

func (a *api) entities(ctx context.Context, ids []string) (map[string]entity, error) {
	result := map[string]entity{}
	for len(ids) > 0 {
		n := len(ids)
		if n > 50 {
			n = 50
		}
		var response struct {
			Entities map[string]entity `json:"entities"`
		}
		err := a.get(ctx, a.wikidata, url.Values{"action": {"wbgetentities"}, "ids": {strings.Join(ids[:n], "|")}, "props": {"claims|sitelinks"}}, &response)
		if err != nil {
			return nil, err
		}
		for id, e := range response.Entities {
			result[id] = e
		}
		ids = ids[n:]
	}
	return result, nil
}

// Candidate search never establishes identity: claims must independently verify
// scientific name, species rank, and the catalogue's immediate parent name.
func (a *api) match(ctx context.Context, name, parent string) (entity, string, error) {
	var search struct {
		Search []struct {
			ID string `json:"id"`
		} `json:"search"`
		Continue *int `json:"search-continue"`
	}
	err := a.get(ctx, a.wikidata, url.Values{"action": {"wbsearchentities"}, "search": {name}, "language": {"en"}, "type": {"item"}, "limit": {"50"}}, &search)
	if err != nil {
		return entity{}, "", err
	}
	if search.Continue != nil {
		return entity{}, "ambiguous", nil
	}
	ids := make([]string, 0, len(search.Search))
	for _, item := range search.Search {
		ids = append(ids, item.ID)
	}
	if len(ids) == 0 {
		return entity{}, "missing", nil
	}
	entities, err := a.entities(ctx, ids)
	if err != nil {
		return entity{}, "", err
	}
	candidates := []entity{}
	parentIDs := []string{}
	seen := map[string]bool{}
	for _, id := range ids {
		e := entities[id]
		if !has(stringsOf(e, "P225"), name) || !has(idsOf(e, "P105"), "Q7432") {
			continue
		}
		candidates = append(candidates, e)
		for _, pid := range idsOf(e, "P171") {
			if !seen[pid] {
				seen[pid] = true
				parentIDs = append(parentIDs, pid)
			}
		}
	}
	if len(candidates) == 0 {
		return entity{}, "missing", nil
	}
	if parent == "" || len(parentIDs) == 0 {
		return entity{}, "ambiguous", nil
	}
	parents, err := a.entities(ctx, parentIDs)
	if err != nil {
		return entity{}, "", err
	}
	matched := []entity{}
	for _, e := range candidates {
		for _, id := range idsOf(e, "P171") {
			if has(stringsOf(parents[id], "P225"), parent) {
				matched = append(matched, e)
				break
			}
		}
	}
	if len(matched) == 1 {
		return matched[0], "matched", nil
	}
	if len(matched) > 1 || len(candidates) > 0 {
		return entity{}, "ambiguous", nil
	}
	return entity{}, "missing", nil
}

type description struct {
	Language, Content, SourceURL, License, Attribution string
	RevisionID                                         int64
}
type image struct{ ImageURL, SourceURL, Creator, License, LicenseURL, Attribution string }

func (a *api) article(ctx context.Context, language, title string) (*description, error) {
	var data struct {
		Query struct {
			Pages []struct {
				Missing   bool   `json:"missing"`
				Extract   string `json:"extract"`
				FullURL   string `json:"fullurl"`
				Revisions []struct {
					Revid int64 `json:"revid"`
				} `json:"revisions"`
			} `json:"pages"`
		} `json:"query"`
	}
	err := a.get(ctx, fmt.Sprintf(a.wikipedia, language), url.Values{"action": {"query"}, "prop": {"extracts|info|revisions"}, "exintro": {"1"}, "explaintext": {"1"}, "inprop": {"url"}, "rvprop": {"ids"}, "redirects": {"1"}, "titles": {title}}, &data)
	if err != nil {
		return nil, err
	}
	if len(data.Query.Pages) != 1 {
		return nil, errors.New("unexpected Wikipedia page response")
	}
	p := data.Query.Pages[0]
	if p.Missing || strings.TrimSpace(p.Extract) == "" {
		return nil, nil
	}
	if p.FullURL == "" || len(p.Revisions) == 0 || p.Revisions[0].Revid == 0 {
		return nil, errors.New("Wikipedia article lacks source or revision")
	}
	return &description{Language: language, Content: p.Extract, SourceURL: p.FullURL, RevisionID: p.Revisions[0].Revid, License: "https://creativecommons.org/licenses/by-sa/4.0/", Attribution: "Wikipedia contributors; article and revision history: " + p.FullURL}, nil
}

var tags = regexp.MustCompile(`<[^>]*>`)

func plain(s string) string {
	return strings.Join(strings.Fields(html.UnescapeString(tags.ReplaceAllString(s, " "))), " ")
}
func allowedLicense(s string) bool {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" {
		return false
	}
	if u.Hostname() == "creativecommons.org" {
		return strings.HasPrefix(u.Path, "/licenses/by/") || strings.HasPrefix(u.Path, "/licenses/by-sa/") || strings.HasPrefix(u.Path, "/publicdomain/zero/") || strings.HasPrefix(u.Path, "/publicdomain/mark/")
	}
	return false
}
func (a *api) photo(ctx context.Context, filename string) (*image, error) {
	var data struct {
		Query struct {
			Pages []struct {
				Missing   bool `json:"missing"`
				ImageInfo []struct {
					URL            string `json:"url"`
					DescriptionURL string `json:"descriptionurl"`
					Metadata       map[string]struct {
						Value json.RawMessage `json:"value"`
					} `json:"extmetadata"`
				} `json:"imageinfo"`
			} `json:"pages"`
		} `json:"query"`
	}
	err := a.get(ctx, a.commons, url.Values{"action": {"query"}, "prop": {"imageinfo"}, "iiprop": {"url|extmetadata"}, "titles": {"File:" + filename}}, &data)
	if err != nil {
		return nil, err
	}
	if len(data.Query.Pages) != 1 || data.Query.Pages[0].Missing || len(data.Query.Pages[0].ImageInfo) == 0 {
		return nil, nil
	}
	info := data.Query.Pages[0].ImageInfo[0]
	meta := info.Metadata
	value := func(key string) string {
		var s string
		_ = json.Unmarshal(meta[key].Value, &s)
		return s
	}
	creator := plain(value("Artist"))
	license := plain(value("LicenseShortName"))
	licenseURL := strings.TrimSpace(value("LicenseUrl"))
	// AttributionRequired can specify an explicit credit beyond the artist.
	credit := plain(value("Attribution"))
	if credit == "" {
		credit = creator
	}
	imgURL, e1 := url.Parse(info.URL)
	sourceURL, e2 := url.Parse(info.DescriptionURL)
	if e1 != nil || e2 != nil || imgURL.Scheme != "https" || imgURL.Hostname() != "upload.wikimedia.org" || sourceURL.Scheme != "https" || sourceURL.Hostname() != "commons.wikimedia.org" || creator == "" || license == "" || credit == "" || !allowedLicense(licenseURL) {
		return nil, nil
	}
	return &image{ImageURL: info.URL, SourceURL: info.DescriptionURL, Creator: creator, License: license, LicenseURL: licenseURL, Attribution: credit}, nil
}
