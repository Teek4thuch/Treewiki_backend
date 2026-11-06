package enrichment

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMatchRequiresNameRankAndParent(t *testing.T) {
	for _, tc := range []struct {
		name, parent string
		duplicate    bool
		want         string
	}{
		{"Mangifera indica", "Mangifera", false, "matched"},
		{"Mangifera indica", "Other", false, "ambiguous"},
		{"Mangifera indica", "", false, "ambiguous"},
		{"Other plant", "Mangifera", false, "missing"},
		{"Mangifera indica", "Mangifera", true, "ambiguous"},
	} {
		t.Run(fmt.Sprint(tc), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("action") == "wbsearchentities" {
					if tc.duplicate {
						fmt.Fprint(w, `{"search":[{"id":"Q1"},{"id":"Q2"}]}`)
					} else {
						fmt.Fprint(w, `{"search":[{"id":"Q1"}]}`)
					}
					return
				}
				if strings.Contains(r.URL.Query().Get("ids"), "Q9") {
					fmt.Fprint(w, `{"entities":{"Q9":{"id":"Q9","claims":{"P225":[{"mainsnak":{"datavalue":{"value":"Mangifera"}}}]}}}}`)
					return
				}
				item := `{"id":"%s","claims":{"P225":[{"mainsnak":{"datavalue":{"value":"Mangifera indica"}}}],"P105":[{"mainsnak":{"datavalue":{"value":{"id":"Q7432"}}}}],"P171":[{"mainsnak":{"datavalue":{"value":{"id":"Q9"}}}}]}}`
				body := fmt.Sprintf(`{"entities":{"Q1":`+item, "Q1")
				if tc.duplicate {
					body += fmt.Sprintf(`,"Q2":`+item, "Q2")
				}
				fmt.Fprint(w, body+`}}`)
			}))
			defer server.Close()
			a := newAPI("TreeWiki test")
			a.wikidata = server.URL
			a.interval = 0
			_, status, err := a.match(context.Background(), tc.name, tc.parent)
			if err != nil || status != tc.want {
				t.Fatalf("got %q %v, want %q", status, err, tc.want)
			}
		})
	}
}

func TestPhotoRequiresUsableMetadata(t *testing.T) {
	for _, tc := range []struct {
		artist, licenseURL string
		want               bool
	}{
		{"Jane", "https://creativecommons.org/licenses/by/4.0/", true},
		{"", "https://creativecommons.org/licenses/by/4.0/", false},
		{"Jane", "https://creativecommons.org/licenses/by-nc/4.0/", false},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"query":{"pages":[{"imageinfo":[{"url":"https://upload.wikimedia.org/a.jpg","descriptionurl":"https://commons.wikimedia.org/wiki/File:a.jpg","extmetadata":{"Artist":{"value":%q},"LicenseShortName":{"value":"CC BY 4.0"},"LicenseUrl":{"value":%q},"CommonsMetadataExtension":{"value":1}}}]}]}}`, tc.artist, tc.licenseURL)
		}))
		a := newAPI("test")
		a.commons = server.URL
		a.interval = 0
		im, err := a.photo(context.Background(), "a.jpg")
		server.Close()
		if err != nil || (im != nil) != tc.want {
			t.Fatalf("%+v: image=%+v err=%v", tc, im, err)
		}
	}
}

func TestArticleMissingAndFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("titles") {
		case "missing":
			fmt.Fprint(w, `{"query":{"pages":[{"missing":true}]}}`)
		case "valid":
			fmt.Fprint(w, `{"query":{"pages":[{"extract":"Plant text","fullurl":"https://en.wikipedia.org/wiki/A","revisions":[{"revid":42}]}]}}`)
		default:
			http.Error(w, "bad", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	a := newAPI("test")
	a.wikipedia = server.URL + "/%s"
	a.interval = 0
	for _, tc := range []struct {
		title           string
		present, failed bool
	}{{"missing", false, false}, {"valid", true, false}, {"bad", false, true}} {
		d, err := a.article(context.Background(), "en", tc.title)
		if (d != nil) != tc.present || (err != nil) != tc.failed {
			t.Fatalf("%s: %+v %v", tc.title, d, err)
		}
	}
}
