// Package mdblist reads Source Lists from the mdblist.com API.
package mdblist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// DefaultBaseURL is the mdblist.com API.
const DefaultBaseURL = "https://api.mdblist.com"

// pageSize is the most items the API returns per request, whatever limit is asked for.
const pageSize = 1000

// Item is one title on a Source List.
type Item struct {
	// TMDBID is the TMDb movie id for movies and the TMDb TV id for Shows.
	TMDBID int64 `json:"id"`
	// TVDBID is 0 when mdblist doesn't know it.
	TVDBID int64  `json:"tvdb_id"`
	Title  string `json:"title"`
	Year   int    `json:"release_year"`
}

// Items is a Source List split by media type.
type Items struct {
	Movies []Item `json:"movies"`
	Shows  []Item `json:"shows"`
}

// Client reads lists from the mdblist.com API.
type Client struct {
	http      *http.Client
	baseURL   string
	apiKey    string
	userAgent string
}

// New returns a Client for the API at baseURL that identifies itself as userAgent.
func New(httpClient *http.Client, baseURL, apiKey, userAgent string) *Client {
	return &Client{http: httpClient, baseURL: baseURL, apiKey: apiKey, userAgent: userAgent}
}

// ListItems returns every item on the list with the given mdblist list id.
func (c *Client) ListItems(ctx context.Context, listID int64) (Items, error) {
	var all Items

	for offset := 0; ; {
		page, hasMore, err := c.page(ctx, listID, offset)
		if err != nil {
			return Items{}, err
		}

		all.Movies = append(all.Movies, page.Movies...)
		all.Shows = append(all.Shows, page.Shows...)

		// Advancing by what arrived, not by the limit asked for, can't skip items if the
		// API returns short pages; an empty page ends the loop whatever X-Has-More says.
		received := len(page.Movies) + len(page.Shows)
		if !hasMore || received == 0 {
			return all, nil
		}

		offset += received
	}
}

func (c *Client) page(ctx context.Context, listID int64, offset int) (Items, bool, error) {
	query := url.Values{
		"apikey": {c.apiKey},
		"limit":  {strconv.Itoa(pageSize)},
		"offset": {strconv.Itoa(offset)},
	}
	endpoint := c.baseURL + "/lists/" + strconv.FormatInt(listID, 10) + "/items?" + query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Items{}, false, fmt.Errorf("list %d: build request: %w", listID, err)
	}

	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return Items{}, false, fmt.Errorf("list %d: %w", listID, withoutURL(err))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		const maxBody = 512

		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))

		return Items{}, false, fmt.Errorf("list %d: HTTP %d: %s", listID, resp.StatusCode, body)
	}

	var items Items
	if err = json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return Items{}, false, fmt.Errorf("list %d: decode response: %w", listID, err)
	}

	return items, resp.Header.Get("X-Has-More") == "true", nil
}

// withoutURL drops the request URL from transport errors because it carries the API key.
func withoutURL(err error) error {
	if urlErr, ok := errors.AsType[*url.Error](err); ok {
		return fmt.Errorf("%s: %w", urlErr.Op, urlErr.Err)
	}

	return err
}
