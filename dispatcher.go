package ilink

import (
	"regexp"
	"strings"
	"sync"
)

// Matcher is a predicate that decides whether a handler should run for a message.
type Matcher func(*Message) bool

type route struct {
	matcher  Matcher
	handlers []HandlerFunc
}

// dispatcher routes incoming messages to handlers.
// Global middleware runs first for every message, then all matching routes run in order.
type dispatcher struct {
	mu         sync.RWMutex
	middleware []HandlerFunc
	routes     []route
}

func newDispatcher() *dispatcher {
	return &dispatcher{}
}

// use registers global middleware that runs before any route handler.
func (d *dispatcher) use(handlers ...HandlerFunc) {
	d.mu.Lock()
	d.middleware = append(d.middleware, handlers...)
	d.mu.Unlock()
}

// add registers a route with the given matcher and handlers.
func (d *dispatcher) add(m Matcher, handlers ...HandlerFunc) {
	d.mu.Lock()
	d.routes = append(d.routes, route{matcher: m, handlers: handlers})
	d.mu.Unlock()
}

// dispatch builds the handler chain for msg and runs it.
// The chain is: [global middleware...] + [handlers of all matching routes...].
func (d *dispatcher) dispatch(ctx *Context) {
	d.mu.RLock()
	middleware := d.middleware
	routes := d.routes
	d.mu.RUnlock()

	var chain []HandlerFunc
	chain = append(chain, middleware...)
	for _, r := range routes {
		if r.matcher(ctx.Message) {
			chain = append(chain, r.handlers...)
		}
	}

	if len(chain) == 0 {
		return
	}
	ctx.handlers = chain
	ctx.index = -1
	ctx.Next()
}

// --- Built-in matchers ---

func matchAll() Matcher                   { return func(*Message) bool { return true } }
func matchText() Matcher                  { return func(m *Message) bool { return m.IsText() } }
func matchImage() Matcher                 { return func(m *Message) bool { return m.IsImage() } }
func matchVoice() Matcher                 { return func(m *Message) bool { return m.IsVoice() } }
func matchFile() Matcher                  { return func(m *Message) bool { return m.IsFile() } }
func matchVideo() Matcher                 { return func(m *Message) bool { return m.IsVideo() } }
func matchGroup() Matcher                 { return func(m *Message) bool { return m.IsGroup() } }
func matchPrivate() Matcher               { return func(m *Message) bool { return m.IsPrivate() } }

func matchTextContains(substr string) Matcher {
	return func(m *Message) bool { return strings.Contains(m.Text(), substr) }
}

func matchTextPrefix(prefix string) Matcher {
	return func(m *Message) bool { return strings.HasPrefix(m.Text(), prefix) }
}

func matchTextRegex(pattern string) Matcher {
	re := regexp.MustCompile(pattern)
	return func(m *Message) bool { return re.MatchString(m.Text()) }
}

func matchGroupID(groupID string) Matcher {
	return func(m *Message) bool { return m.GroupID == groupID }
}

func matchUserID(userID string) Matcher {
	return func(m *Message) bool { return m.FromUserID == userID }
}

// andMatch combines two matchers with logical AND.
func andMatch(a, b Matcher) Matcher {
	return func(m *Message) bool { return a(m) && b(m) }
}
