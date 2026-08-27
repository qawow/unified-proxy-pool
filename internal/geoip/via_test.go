package geoip

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestParseExitBodyShapes(t *testing.T) {
	cases := []struct {
		name string
		body string
		code string
		ip   string
	}{
		{"ip-api", `{"status":"success","country":"China","countryCode":"CN","query":"1.2.3.4"}`, "CN", "1.2.3.4"},
		{"ipwho", `{"success":true,"country":"United States","country_code":"US","ip":"8.8.8.8"}`, "US", "8.8.8.8"},
		{"httpbin origin list", `{"origin":"9.9.9.9, 1.1.1.1"}`, "", "9.9.9.9"},
		{"ipify json", `{"ip":"4.4.4.4"}`, "", "4.4.4.4"},
		{"中国 spelling", `{"status":"success","country":"中国","countryCode":"CN"}`, "CN", ""},
	}
	for _, tc := range cases {
		r, err := ParseExitBody([]byte(tc.body))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if tc.code != "" && r.CountryCode != tc.code {
			t.Errorf("%s code = %q, want %q", tc.name, r.CountryCode, tc.code)
		}
		if tc.ip != "" && r.Query != tc.ip {
			t.Errorf("%s query = %q, want %q", tc.name, r.Query, tc.ip)
		}
	}
}

func TestParseExitBodyRejectsFail(t *testing.T) {
	if _, err := ParseExitBody([]byte(`{"status":"fail","message":"private range"}`)); err == nil {
		t.Fatal("expected error for failed ip-api status")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestLookupViaUsesSelfURLAndParses(t *testing.T) {
	s := New(nil)
	s.selfURLs = []string{"http://geo.test/json"}
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "geo.test" {
			t.Errorf("host = %s, want geo.test (via-proxy self lookup)", req.URL.Host)
		}
		body := `{"status":"success","country":"Germany","countryCode":"DE","query":"5.6.7.8"}`
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})
	r, err := s.LookupVia(context.Background(), rt)
	if err != nil {
		t.Fatal(err)
	}
	if r.CountryCode != "DE" || r.Query != "5.6.7.8" {
		t.Fatalf("got %+v", r)
	}
}
