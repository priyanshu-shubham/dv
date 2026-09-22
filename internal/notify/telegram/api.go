package telegram

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"time"
)

// botAPI is Telegram's Bot API, as much of it as dv uses.
type botAPI struct {
	base string
	http *http.Client
}

// Longer than the long poll getUpdates holds a request for.
const callTimeout = 40 * time.Second

// pollFor is how long getUpdates holds a request open, in seconds.
const pollFor = 25

type apiError struct {
	Description string
	RetryAfter  int
}

func (e *apiError) Error() string { return "Telegram said: " + e.Description }

func (b botAPI) call(ctx context.Context, token, method string, params, out any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return b.send(ctx, token, method, "application/json", body, out)
}

// upload makes a call with a file in it, which goes as multipart form data:
// fields as they are, and the file under its name.
func (b botAPI) upload(ctx context.Context, token, method string, fields map[string]string, name string, file []byte) error {
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for k, v := range fields {
		form.WriteField(k, v)
	}
	part, err := form.CreateFormFile(name, name+".jpg")
	if err != nil {
		return err
	}
	part.Write(file)
	form.Close()
	return b.send(ctx, token, method, form.FormDataContentType(), body.Bytes(), nil)
}

func (b botAPI) send(ctx context.Context, token, method, kind string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.base+"/bot"+token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return errors.New("that is not a bot token")
	}
	req.Header.Set("Content-Type", kind)
	resp, err := b.http.Do(req)
	if err != nil {
		return unreached(err)
	}
	defer resp.Body.Close()
	var r struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return fmt.Errorf("Telegram answered %s", resp.Status)
	}
	if !r.OK {
		return &apiError{r.Description, r.Parameters.RetryAfter}
	}
	if out != nil {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

// download is a file the reader sent, which Telegram first says where to get.
func (b botAPI) download(ctx context.Context, token, id string) ([]byte, error) {
	var f struct {
		Path string `json:"file_path"`
	}
	if err := b.call(ctx, token, "getFile", map[string]string{"file_id": id}, &f); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.base+"/file/bot"+token+"/"+f.Path, nil)
	if err != nil {
		return nil, errors.New("Telegram gave a file dv cannot fetch")
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, unreached(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Telegram answered %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxFetch))
}

// maxFetch is all Telegram gives a bot of a file.
const maxFetch = 20 << 20

// unreached is an error that has the token in it, in the URL, without it: it
// is not for logs or pages.
func unreached(err error) error {
	if u := (*url.Error)(nil); errors.As(err, &u) {
		return fmt.Errorf("could not reach Telegram: %w", u.Err)
	}
	return err
}

type update struct {
	ID       int64     `json:"update_id"`
	Message  *message  `json:"message"`
	Callback *callback `json:"callback_query"`
}

type message struct {
	ID     int64 `json:"message_id"`
	Thread int64 `json:"message_thread_id"`
	Date   int64 `json:"date"`
	Chat   struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	} `json:"chat"`
	From     *user       `json:"from"`
	Text     string      `json:"text"`
	Caption  string      `json:"caption"` // a picture's words
	ReplyTo  *message    `json:"reply_to_message"`
	Album    string      `json:"media_group_id"` // pictures sent together share one
	Photo    []photoSize `json:"photo"`
	Document *document   `json:"document"`
	// Files Telegram sends as media of their own, which go as files too.
	Voice     *document `json:"voice"`
	Video     *document `json:"video"`
	VideoNote *document `json:"video_note"`
	Animation *document `json:"animation"`
	Audio     *document `json:"audio"`
	Sticker   any       `json:"sticker"` // which dv does not take

	more []*message // the rest of an album, gathered on its first
}

// sent is whether the reader sent the message, rather than Telegram noting
// something in the chat, a topic made, say.
func (m *message) sent() bool {
	return m.Text != "" || len(m.Photo) > 0 || m.Document != nil || m.Sticker != nil || m.media() != nil
}

// media is the file a message carries other than a document, with the name
// it goes by when Telegram gives it none.
func (m *message) media() *document {
	for _, d := range []struct {
		doc  *document
		name string
	}{{m.Video, "video.mp4"}, {m.VideoNote, "video.mp4"}, {m.Animation, "animation.mp4"}, {m.Audio, "audio.mp3"}, {m.Voice, "voice.ogg"}} {
		if d.doc != nil {
			f := *d.doc
			f.Name = cmp.Or(f.Name, d.name)
			return &f
		}
	}
	return nil
}

// A photo comes in several sizes, the smallest first.
type photoSize struct {
	FileID string `json:"file_id"`
	Size   int    `json:"file_size"`
}

// document is a file sent as it is, as a picture can be.
type document struct {
	FileID   string `json:"file_id"`
	MimeType string `json:"mime_type"`
	Size     int    `json:"file_size"`
	Name     string `json:"file_name"` // a voice note's has none
}

type user struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
	Topics    bool   `json:"has_topics_enabled"` // a bot's, in its private chats
}

type callback struct {
	ID      string   `json:"id"`
	From    user     `json:"from"`
	Message *message `json:"message"`
	Data    string   `json:"data"`
}

type button struct {
	Text string `json:"text"`
	Data string `json:"callback_data,omitempty"`
	URL  string `json:"url,omitempty"`
}
