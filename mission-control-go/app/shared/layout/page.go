package layout

import (
	"net/http"

	"github.com/a-h/templ"
	"strconv"
	"time"

	"github.com/scttymn/gantry/web"
)

// Page is what the layout needs from a page.
type Page struct {
	// Title is the page's; "" is Mission Control's own.
	Title string
	// NoHeader is a page with nothing to navigate to yet (sign-in).
	NoHeader bool
	// Setup is a first-run step's number (1 to 3): the bar shows the steps
	// and Host, the address the admin reached, instead.
	Setup int
	Host  string
	Chrome   Chrome
	Toast    *Toast
	// Head is what the page adds to the head (Rails' content_for :head).
	Head templ.Component
	// Zone is the request's time zone (the installation's), for showing
	// times.
	Zone *time.Location
}

func (p Page) title() string {
	if p.Title == "" {
		return "Mission Control"
	}
	return p.Title
}

// Chrome is what the top bar shows.
type Chrome struct {
	SignedIn bool
	// Settings is a Settings page: its link is the current one.
	Settings bool
	// Status is the systems' status, once setup is done.
	Status *Status
	// Version is the Houston this is.
	Version string
	// Shadow is a read-only preview.
	Shadow bool
}

// Status is the systems' status: the tunnel, the runners heard from in the
// last minute (of Expected; 0: not shown), the registry, and the time.
type Status struct {
	Tunnel, Registry  bool
	Runners, Expected int
	Clock             string
}

func (s Status) runners() string { return strconv.Itoa(s.Runners) + "/" + strconv.Itoa(s.Expected) }

// Toast is what just happened: GO, or NO-GO.
type Toast struct {
	NoGo bool
	Text string
}

// ChromeKey is where the page filter keeps the top bar's contents.
var ChromeKey = web.NewKey[Chrome]("chrome")

// For is a page titled title, with the top bar the page filter made for
// the request.
func For(r *http.Request, title string) Page {
	c, _ := web.Get(r, ChromeKey)
	return Page{Title: title, Chrome: c, Zone: web.Zone(r)}
}

// SetupStep is first run's step (1 to 3), titled title.
func SetupStep(r *http.Request, title string, step int) Page {
	return Page{Title: title, Setup: step, Host: web.RequestHost(r), Zone: web.Zone(r)}
}
