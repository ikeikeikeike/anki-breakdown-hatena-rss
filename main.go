package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"
	ext "github.com/mmcdole/gofeed/extensions"
	"golang.org/x/xerrors"
)

type args struct {
	URL string
}

func parseArgs() (*args, error) {
	a := &args{}

	flag.StringVar(&a.URL, "url", "", "e.g. https://b.hatena.ne.jp/ikeikeikeike/bookmark.rss")
	flag.Parse()

	seen := make(map[string]bool)
	flag.Visit(func(f *flag.Flag) { seen[f.Name] = true })
	for _, r := range []string{"url"} {
		if !seen[r] {
			return nil, xerrors.Errorf("missing required -%s argument/flag", r)
		}
	}

	return a, nil
}

func main() {
	// Arguments
	args, err := parseArgs()
	if err != nil {
		panic(err) // Panic for simply script
	}
	fp := gofeed.NewParser()
	fp.UserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

	feed, err := fp.ParseURL(args.URL)
	if err != nil {
		panic(err) // Panic for simply script
	}
	ctx := context.Background()

	a := newAnki()
	for _, item := range feed.Items {
		front := fmt.Sprintf(`
<p>Break it down?</p>
<hr />
<br />

<img src="%s" />
<p>%s</p>%s
<p>%s</p>
<br />
<div style="text-align: left;">
	<p>Bookmark: %s Users</p>
	<p>Date: %s</p>
  <p>%s: %s</p>
</div>
`,
			item.Image.URL,
			item.Title,
			strings.Join(item.Categories, " "),
			item.Link,
			safeGetBookmarkCount(item.Extensions),
			item.PublishedParsed.In(time.Local).Format("2006-01-02 15:04:05"),
			item.Author.Name,
			item.Description,
		)

		r, err := a.AddNote(ctx, front, item.Content, item.Categories)
		switch {
		case err != nil:
			fmt.Printf("ERR: %+v\n", err) // simply
		case r.Error == "":
			fmt.Printf("OK: %+v\n", r) // simply
		default:
			fmt.Printf("NG: %+v\n", r) // simply
		}
	}
}

func newAnki() Anki {
	cl := &http.Client{
		Transport: &http.Transport{
			// TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			// TLSHandshakeTimeout: 60 * time.Second,
		},
	}
	return &anki{
		cl:   cl,
		host: "http://127.0.0.1:8765",
		deck: "Hatena",
	}
}

type (
	// Anki core function
	Anki interface {
		AddNote(ctx context.Context, front, back string, tags []string) (*addNoteResult, error)
	}

	anki struct {
		cl   *http.Client
		host string
		deck string
	}

	addNoteResult struct {
		Result int    `json:"result"`
		Error  string `json:"error"`
	}
)

func (a *anki) AddNote(ctx context.Context, front, back string, tags []string) (*addNoteResult, error) {
	name := "Request"

	data := addNoteData{
		Action:  "addNote",
		Version: 6,
		Params: addNoteParams{
			Note: addNoteNote{
				DeckName:  a.deck,
				ModelName: "Basic",
				Fields: addNoteFields{
					Front: front,
					Back:  back,
				},
				Options: addNoteOptions{
					AllowDuplicate: false,
					DuplicateScope: "deck",
					DuplicateScopeOptions: addNoteDuplicateScopeOptions{
						DeckName:       a.deck,
						CheckChildren:  false,
						CheckAllModels: false,
					},
				},
				Tags:    tags,
				Picture: []addNotePicture{
					// {
					// 	URL:      "https://example.com/image.jpg",
					// 	Filename: "image.jpg",
					// 	SkipHash: "8d6e4646dfae812bf39651b59d7429ce",
					// 	Fields:   []string{"Back"}, // or Front
					// },
				},
			},
		},
	}
	jsonData, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("%s json.Marshal: %w", name, err)
	}

	req, err := http.NewRequest(http.MethodPost, a.host, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, xerrors.Errorf("%s NewRequest: %w", name, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := a.cl.Do(req)
	if err != nil {
		return nil, xerrors.Errorf("%s request.Do: %w", name, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, xerrors.Errorf("%s ReadAll: %w", name, err)
	}
	if resp.StatusCode >= http.StatusInternalServerError {
		return nil, xerrors.Errorf("%s Non-OK HTTP status %d: %s", name, resp.StatusCode, body)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return nil, xerrors.Errorf("%s Non-OK HTTP status %d: %s: %w", name, resp.StatusCode, body, ErrHTTP400)
	}
	// if resp.StatusCode != http.StatusCreated {
	// 	return false, xerrors.Errorf("%s Non-OK HTTP status %d: %s: %w", name, resp.StatusCode, body, errs.ErrGRPCInvalidArgument)
	// }

	r := &addNoteResult{}
	if err := json.Unmarshal(body, r); err != nil {
		return nil, xerrors.Errorf("%s Unmarshal: %w", name, err)
	}

	return r, nil
}

// safeGetBookmarkCount safely retrieves the bookmark count value from the nested map structure.
func safeGetBookmarkCount(extensions ext.Extensions) string {
	if hatena, ok := extensions["hatena"]; ok {
		if bookmarkcount, ok := hatena["bookmarkcount"]; ok {
			if len(bookmarkcount) > 0 {
				return bookmarkcount[0].Value
			}
		}
	}

	return ""
}

var (
	// ErrHTTP400 uses as 400 BadRequest
	ErrHTTP400 = xerrors.New(http.StatusText(http.StatusBadRequest))
)

type (
	addNoteFields struct {
		Front string `json:"Front"`
		Back  string `json:"Back"`
	}

	addNoteDuplicateScopeOptions struct {
		DeckName       string `json:"deckName"`
		CheckChildren  bool   `json:"checkChildren"`
		CheckAllModels bool   `json:"checkAllModels"`
	}

	addNoteOptions struct {
		AllowDuplicate        bool                         `json:"allowDuplicate"`
		DuplicateScope        string                       `json:"duplicateScope"`
		DuplicateScopeOptions addNoteDuplicateScopeOptions `json:"duplicateScopeOptions"`
	}

	addNotePicture struct {
		URL      string   `json:"url"`
		Filename string   `json:"filename"`
		SkipHash string   `json:"skipHash"`
		Fields   []string `json:"fields"`
	}

	addNoteNote struct {
		DeckName  string           `json:"deckName"`
		ModelName string           `json:"modelName"`
		Fields    addNoteFields    `json:"fields"`
		Options   addNoteOptions   `json:"options"`
		Tags      []string         `json:"tags"`
		Picture   []addNotePicture `json:"picture"`
	}

	addNoteParams struct {
		Note addNoteNote `json:"note"`
	}

	addNoteData struct {
		Action  string        `json:"action"`
		Version int           `json:"version"`
		Params  addNoteParams `json:"params"`
	}
)
