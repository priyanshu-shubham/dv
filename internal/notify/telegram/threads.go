package telegram

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

// inThread sends to a session's thread, made if need be, through send. A
// thread the reader deleted is made again. Should the bot have no topics,
// send is given 0, the chat itself.
func (t *Telegram) inThread(ctx context.Context, cfg Config, folder, session, title, place string, send func(thread int64) error) error {
	thread := t.thread(ctx, cfg, folder, session, title, place)
	err := send(thread)
	if e := (*apiError)(nil); thread != 0 && errors.As(err, &e) && strings.Contains(e.Description, "thread not found") {
		t.change(func(c *Config) { delete(c.Threads, threadKey(folder, session)) })
		err = send(t.thread(ctx, t.config(), folder, session, title, place))
	}
	return err
}

// thread is a session's topic in the chat, made the first time it is needed
// and renamed once the session has a title, or another. title may be ""
// before the session has one.
func (t *Telegram) thread(ctx context.Context, cfg Config, folder, session, title, place string) int64 {
	t.threading.Lock()
	defer t.threading.Unlock()
	k := threadKey(folder, session)
	name := topicName(title, place)
	if th, ok := t.config().Threads[k]; ok {
		if title != "" && th.Name != name {
			err := t.call(ctx, cfg, "editForumTopic", map[string]any{"chat_id": cfg.Chat, "message_thread_id": th.ID, "name": name}, nil)
			if err == nil {
				t.change(func(c *Config) {
					if _, still := c.Threads[k]; still {
						c.Threads[k] = Thread{th.ID, name}
					}
				})
			}
		}
		return th.ID
	}
	var topic struct {
		ID int64 `json:"message_thread_id"`
	}
	if err := t.call(ctx, cfg, "createForumTopic", map[string]any{"chat_id": cfg.Chat, "name": name}, &topic); err != nil {
		fmt.Fprintln(os.Stderr, "dv: could not make a thread in Telegram:", err)
		return 0
	}
	t.change(func(c *Config) {
		if c.Threads == nil {
			c.Threads = map[string]Thread{}
		}
		c.Threads[k] = Thread{topic.ID, name}
	})
	return topic.ID
}

// adopt makes a thread the reader started - Telegram makes one for a message
// written outside any - a session's, named for it.
func (t *Telegram) adopt(ctx context.Context, cfg Config, thread int64, folder, session, title, place string) {
	t.threading.Lock()
	defer t.threading.Unlock()
	name := topicName(title, place)
	t.call(ctx, cfg, "editForumTopic", map[string]any{"chat_id": cfg.Chat, "message_thread_id": thread, "name": name}, nil)
	t.change(func(c *Config) {
		if c.Threads == nil {
			c.Threads = map[string]Thread{}
		}
		c.Threads[threadKey(folder, session)] = Thread{thread, name}
	})
}

// sessionOf is the session a thread is, if it is one.
func (t *Telegram) sessionOf(thread int64) (folder, session string, ok bool) {
	if thread == 0 {
		return "", "", false
	}
	for k, th := range t.config().Threads {
		if th.ID == thread {
			folder, session, _ = strings.Cut(k, "/")
			return folder, session, true
		}
	}
	return "", "", false
}

func threadKey(folder, session string) string { return folder + "/" + session }

// topicName is what a session's thread is called: its title, and the folder
// it is in. Telegram takes 128 characters.
func topicName(title, place string) string {
	name := clip(strings.Join(strings.Fields(title), " "), 96)
	if name == "" {
		name = "New session"
	}
	if place != "" {
		name += " · " + clip(place, 28)
	}
	return name
}

// split cuts Markdown into parts of at most size bytes, between lines. A code
// block cut in two is closed at the end of one part and opened again at the
// start of the next.
func split(md string, size int) []string {
	var parts []string
	var b strings.Builder
	open := "" // the line that opened the code block the part is in
	for _, line := range strings.Split(strings.TrimSpace(md), "\n") {
		if b.Len() > 0 && b.Len()+len(line)+5 > size {
			part := b.String()
			if open != "" {
				part += open[:3]
			}
			parts = append(parts, clip(strings.TrimSpace(part), size))
			b.Reset()
			if open != "" {
				b.WriteString(open + "\n")
			}
		}
		if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			if open == "" {
				open = trimmed
			} else if strings.HasPrefix(trimmed, open[:3]) {
				open = ""
			}
		}
		b.WriteString(line + "\n")
	}
	if rest := strings.TrimSpace(b.String()); rest != "" {
		parts = append(parts, clip(rest, size))
	}
	return parts
}
