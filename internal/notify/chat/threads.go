package chat

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// inThread sends to a session's thread, made if need be, through send. A
// thread the reader deleted is made again. In an app without threads, send
// is given "", the chat itself.
func (c *Conversation) inThread(ctx context.Context, folder, session, title, place string, send func(thread string) error) error {
	thread := c.thread(ctx, folder, session, title, place)
	err := send(thread)
	if thread != "" && err != nil && c.p.Gone(err) {
		c.p.DropThread(threadKey(folder, session))
		err = send(c.thread(ctx, folder, session, title, place))
	}
	return err
}

// thread is a session's in the chat, made the first time it is needed and
// renamed once the session has a title, or another. title may be "" before
// the session has one.
func (c *Conversation) thread(ctx context.Context, folder, session, title, place string) string {
	c.threading.Lock()
	defer c.threading.Unlock()
	k := threadKey(folder, session)
	name := topicName(title, place)
	if th, ok := c.p.Thread(k); ok {
		if title != "" && th.Name != name && c.p.Rename(ctx, th.ID, name) == nil {
			c.p.SetThread(k, Thread{th.ID, name})
		}
		return th.ID
	}
	id, err := c.p.NewThread(ctx, name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dv: could not make a thread in %s: %v\n", c.p.Via(), err)
		return ""
	}
	if id != "" {
		c.p.SetThread(k, Thread{id, name})
	}
	return id
}

// adopt makes a thread the reader started a session's - an app may make one
// for a message written outside any - named for it.
func (c *Conversation) adopt(ctx context.Context, thread, folder, session, title, place string) {
	c.threading.Lock()
	defer c.threading.Unlock()
	name := topicName(title, place)
	c.p.Rename(ctx, thread, name)
	c.p.SetThread(threadKey(folder, session), Thread{thread, name})
}

// sessionOf is the session a thread is, if it is one.
func (c *Conversation) sessionOf(thread string) (folder, session string, ok bool) {
	if thread == "" {
		return "", "", false
	}
	k, ok := c.p.ThreadOf(thread)
	if !ok {
		return "", "", false
	}
	folder, session, _ = strings.Cut(k, "/")
	return folder, session, true
}

func threadKey(folder, session string) string { return folder + "/" + session }

// topicName is what a session's thread is called: its title, and the folder
// it is in, in 128 characters, as Telegram takes.
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
