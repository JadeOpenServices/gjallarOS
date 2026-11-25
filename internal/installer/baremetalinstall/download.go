package baremetalinstall

import (
	"bytes"
	"io"
	"net/url"
	"regexp"
)

// downloadWatch passes nixos-install's output through and remembers the
// host of the last download Nix gave up on, so the failure can say in plain
// words what stopped the install (Plane 431). Nix prints the cause itself;
// this only names it once more at the end, under the error line.
type downloadWatch struct {
	out     io.Writer
	partial []byte
	host    string
}

// Nix: "error: unable to download 'https://host/path': <curl text> (<code>)".
var unableToDownload = regexp.MustCompile(`unable to download '([^']+)'`)

// maxWatchLine caps the unfinished line kept between writes. Progress output
// without newlines must not grow it without end.
const maxWatchLine = 8 << 10

func (w *downloadWatch) Write(p []byte) (int, error) {
	n, err := w.out.Write(p)
	buf := append(w.partial, p[:n]...)
	for {
		i := bytes.IndexAny(buf, "\r\n")
		if i < 0 {
			break
		}
		w.look(buf[:i])
		buf = buf[i+1:]
	}
	if len(buf) > maxWatchLine {
		w.look(buf)
		buf = nil
	}
	w.partial = append(w.partial[:0], buf...)
	return n, err
}

func (w *downloadWatch) look(line []byte) {
	m := unableToDownload.FindSubmatch(line)
	if m == nil {
		return
	}
	u, err := url.Parse(string(m[1]))
	if err != nil || u.Hostname() == "" {
		return
	}
	w.host = u.Hostname()
}

// failedHost is the host of the last download Nix gave up on, or "".
func (w *downloadWatch) failedHost() string {
	w.look(w.partial)
	return w.host
}
