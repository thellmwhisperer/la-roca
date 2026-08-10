package oauth

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"time"
)

// DefaultLoginTimeout is how long the login waits for the operator to finish in
// the browser. Generous, because logging in involves a password and maybe a
// second factor, and bounded, because a command that never comes back is a
// command that has to be killed.
const DefaultLoginTimeout = 5 * time.Minute

// LoginOptions is what the surface controls about a login.
type LoginOptions struct {
	// Out is where the address to open is printed. It is always printed, even
	// when the browser does open: on a machine with no browser, or over SSH,
	// that line is the whole flow.
	Out io.Writer
	// OpenBrowser opens the address. Nil is OpenInBrowser.
	OpenBrowser func(address string) error
	Timeout     time.Duration
}

// callback is what the vendor's redirect brought back.
type callback struct {
	code  string
	state string
	err   error
}

// Login runs the browser flow and returns the session.
//
// The whole flow lives in this one function on purpose: it opens a loopback
// listener, prints the address, waits for exactly one callback and closes. There
// is no resident server and nothing survives the command, which is the same
// no-daemon rule the rest of the product follows.
func (f Flow) Login(ctx context.Context, opts LoginOptions) (Token, error) {
	redirect, err := url.Parse(f.Redirect)
	if err != nil {
		return Token{}, fmt.Errorf("the callback address %q is not a URL: %w", f.Redirect, err)
	}

	listener, err := net.Listen("tcp", redirect.Host)
	if err != nil {
		return Token{}, fmt.Errorf(
			"I cannot listen on %s for the vendor's callback: %w. "+
				"Close whatever is using port %s and try again",
			redirect.Host, err, redirect.Port())
	}
	defer listener.Close()

	pkce, err := NewPKCE()
	if err != nil {
		return Token{}, err
	}
	state, err := NewState()
	if err != nil {
		return Token{}, err
	}

	arrived := make(chan callback, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != redirect.Path {
			http.NotFound(w, r)
			return
		}
		query := r.URL.Query()
		if vendorError := query.Get("error"); vendorError != "" {
			reply(w, "Login not completed. You can close this tab and go back to the terminal.")
			select {
			case arrived <- callback{err: fmt.Errorf("the vendor rejected the login: %s%s",
				vendorError, describe(query.Get("error_description")))}:
			default:
			}
			return
		}
		reply(w, "La Roca is now connected. You can close this tab.")
		select {
		case arrived <- callback{code: query.Get("code"), state: query.Get("state")}:
		default:
		}
	})}
	go server.Serve(listener)
	defer server.Close()

	// Tell the operator what is about to happen before anything opens: a
	// browser at the vendor's page is surprising without the two sentences
	// that say what La Roca receives and how to take it back.
	fmt.Fprint(opts.Out, LoginNarrative)

	address := f.AuthorizeURL(pkce, state)
	fmt.Fprintf(opts.Out, "open this address to log in:\n%s\n", address)
	open := opts.OpenBrowser
	if open == nil {
		open = OpenInBrowser
	}
	if err := open(address); err != nil {
		// A browser that does not open is not a broken login: the address is
		// already printed and the operator pastes it.
		fmt.Fprintf(opts.Out, "I could not open the browser (%v): paste the address by hand\n", err)
	}
	fmt.Fprint(opts.Out, LoginWaiting)

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultLoginTimeout
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	select {
	case <-waitCtx.Done():
		return Token{}, fmt.Errorf("the login did not finish within %s: run it again", timeout)
	case got := <-arrived:
		switch {
		case got.err != nil:
			return Token{}, got.err
		case got.state != state:
			// A callback with somebody else's state is not this flow's callback.
			return Token{}, fmt.Errorf("the callback came back with a state that is not this login's: start over")
		case got.code == "":
			return Token{}, fmt.Errorf("the callback came back with no code: start over")
		}
		return f.Exchange(ctx, got.code, pkce.Verifier)
	}
}

// LoginNarrative is what the operator reads before the browser opens. It is a
// constant so the CLI and the golden test share one wording.
const LoginNarrative = "" +
	"A browser will open at the vendor's auth page. Confirm the account there.\n" +
	"La Roca receives an access token, never your password.\n" +
	"The session lives on this machine; revoke it with `roca logout codex`.\n"

// LoginWaiting is the line shown while the callback has not arrived. Ctrl+C
// cancels the command the same way it cancels any other foreground command.
const LoginWaiting = "Waiting for you to finish in the browser (Ctrl+C to cancel)...\n"

func reply(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.WriteString(w, message+"\n")
}

func describe(detail string) string {
	if detail == "" {
		return ""
	}
	return " (" + detail + ")"
}

// OpenInBrowser hands the address to the desktop. It is the only place in this
// product that runs an external command, and it runs the platform's opener with
// the address as a separate argument, never through a shell.
func OpenInBrowser(address string) error {
	var command string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		command, args = "open", []string{address}
	case "windows":
		command, args = "rundll32", []string{"url.dll,FileProtocolHandler", address}
	default:
		command, args = "xdg-open", []string{address}
	}
	path, err := exec.LookPath(command)
	if err != nil {
		return fmt.Errorf("%s is not on this machine", command)
	}
	return exec.Command(path, args...).Start()
}
