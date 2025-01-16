package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/xerrors"

	"github.com/cockroachdb/pebble"
	"github.com/mmcdole/gofeed"
	ext "github.com/mmcdole/gofeed/extensions"
)

type args struct {
	URL  string
	Deck string
}

func parseArgs() (*args, error) {
	a := &args{}

	flag.StringVar(&a.URL, "url", "", "e.g. https://b.hatena.ne.jp/ikeikeikeike/bookmark.rss")
	flag.StringVar(&a.Deck, "deck", "Hatena", "Note's Deckname")
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
		log.Panic(err) // Panic is useful for the simply script
	}
	fp := gofeed.NewParser()
	fp.UserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

	feed, err := fp.ParseURL(args.URL)
	if err != nil {
		log.Panic(err) // Panic is useful for the simply script
	}
	db, err := mewPebble()
	if err != nil {
		log.Panic(err) // Panic is useful for the simply script
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()

	a := newAnki()
	for _, item := range feed.Items {
		key := pebbleKey(args.URL, args.Deck, item.Link)

		value, closer, err := db.Get(key)
		if err != nil && !xerrors.Is(err, pebble.ErrNotFound) {
			log.Printf("Err DB Get: %+v\n", err)
			continue
		}
		if closer != nil {
			if err := closer.Close(); err != nil {
				log.Printf("Err DB Closer: %+v\n", err)
				continue
			}
		}
		if len(value) != 0 {
			log.Printf("NG Dup by: %s:%s:%s\n", args.URL, args.Deck, item.Link)
			continue
		}

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

		r, err := a.AddNote(ctx, front, item.Content, args.Deck, item.Categories)
		if err != nil {
			log.Printf("Err: %+v\n", err)
			continue
		}
		if r.Error != "" {
			log.Printf("NG: %+v\n", r)
			continue
		}

		if err := db.Set(key, []byte(fmt.Sprint(r.Result)), pebble.Sync); err != nil {
			log.Printf("Err DB Write: %+v\n", err)
			continue
		}

		log.Printf("OK: %+v\n", r)
		time.Sleep(100 * time.Millisecond)
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
		host: "http://127.0.0.1:8765", // Addon: https://foosoft.net/projects/anki-connect/
	}
}

type (
	// Anki core function
	Anki interface {
		AddNote(ctx context.Context, front, back, deck string, tags []string) (*addNoteResult, error)
	}

	anki struct {
		cl   *http.Client
		host string
	}

	addNoteResult struct {
		Result int64  `json:"result"`
		Error  string `json:"error"`
	}
)

func (a *anki) AddNote(ctx context.Context, front, back, deck string, tags []string) (*addNoteResult, error) {
	name := "addNote"

	data := addNoteData{
		Action:  name,
		Version: 6,
		Params: addNoteParams{
			Note: addInsideNote{
				DeckName:  deck,
				ModelName: "Basic",
				Fields: addNoteFields{
					Front: front,
					Back:  back,
				},
				Options: addNoteOptions{
					AllowDuplicate: false,
					DuplicateScope: "deck",
					DuplicateScopeOptions: addNoteDuplicateScopeOptions{
						DeckName:       deck,
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

	addInsideNote struct {
		DeckName  string           `json:"deckName"`
		ModelName string           `json:"modelName"`
		Fields    addNoteFields    `json:"fields"`
		Options   addNoteOptions   `json:"options"`
		Tags      []string         `json:"tags"`
		Picture   []addNotePicture `json:"picture"`
	}

	addNoteParams struct {
		Note addInsideNote `json:"note"`
	}

	addNoteData struct {
		Action  string        `json:"action"`
		Version int           `json:"version"`
		Params  addNoteParams `json:"params"`
	}
)

// pebbleKey concatenates the input strings with a colon separator,
// computes the SHA-256 hash of the resulting string, and returns
// the hash as a byte slice.
func pebbleKey(keys ...string) []byte {
	// Concatenate the input strings with a colon separator
	concatenated := strings.Join(keys, ":")
	// Compute the SHA-256 hash of the concatenated string
	hash := sha256.Sum256([]byte(concatenated))
	// Return the hash as a byte slice
	return hash[:]
}

func mewPebble() (*pebble.DB, error) {
	path, err := os.UserHomeDir()
	if err != nil {
		return nil, xerrors.Errorf("pebble UserHomeDir: %w", err)
	}
	path = filepath.Join(path, ".cache/anki-breakdown-hatena-rss")

	db, err := pebble.Open(path, &pebble.Options{})
	if err != nil {
		return nil, xerrors.Errorf("pebble Open: %w", err)
	}

	return db, nil
}
