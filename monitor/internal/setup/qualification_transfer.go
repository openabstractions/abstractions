package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"time"

	request "github.com/openabstractions/abstraction-download/go/abstraction/download/request"
	"github.com/openabstractions/abstraction-facade/go/client"
)

// QualificationMirror names transport for one existing compiled catalogue pin.
// It supplies untrusted bytes; compiled size/hash pins remain authoritative.
// Browser requests cannot supply these transport selections.
type QualificationMirror struct {
	CanonicalURL string `json:"canonical_url"`
	SHA256       string `json:"sha256"`
	Bytes        int64  `json:"bytes"`
	MirrorURL    string `json:"mirror_url"`
}

// QualificationInput is bounded startup DATA for explicit developer HTTP mode.
// Runtime fields select exactly the already verified runtime, without fallback.
type QualificationInput struct {
	RuntimeEndpoint string                `json:"runtime_endpoint"`
	RuntimeProgram  string                `json:"runtime_program"`
	RuntimeStateDir string                `json:"runtime_state_dir"`
	Expires         time.Time             `json:"expires"`
	Mirrors         []QualificationMirror `json:"mirrors"`
}

const QualificationInputMaxBytes = 256 * 1024

func ParseQualificationInput(data []byte) (QualificationInput, error) {
	var input QualificationInput
	if len(data) == 0 || len(data) > QualificationInputMaxBytes {
		return input, errors.New("qualification transport input exceeds its bounded size")
	}
	keys := json.NewDecoder(bytes.NewReader(data))
	if err := checkJSONKeys(keys, exactJSONFields(QualificationInput{})); err != nil {
		return input, err
	}
	if _, err := keys.Token(); !errors.Is(err, io.EOF) {
		return input, errors.New("qualification transport has trailing JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return input, err
	}
	var raw struct {
		Mirrors []json.RawMessage `json:"mirrors"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return input, err
	}
	for _, mirror := range raw.Mirrors {
		if err := checkJSONKeys(json.NewDecoder(bytes.NewReader(mirror)), exactJSONFields(QualificationMirror{})); err != nil {
			return input, err
		}
	}
	if input.RuntimeEndpoint == "" || input.RuntimeProgram == "" || input.RuntimeStateDir == "" || len(input.RuntimeEndpoint) > 4096 || len(input.RuntimeProgram) > 4096 || len(input.RuntimeStateDir) > 4096 {
		return input, errors.New("qualification transport needs the exact runtime selection")
	}
	return input, nil
}

type qualificationPin struct {
	URL, SHA256 string
	Bytes       int64
}

type qualificationSources struct {
	expires time.Time
	mirrors map[qualificationPin]string
}

// NewQualificationTransfer is an internal native-receiving constructor. It
// preserves compiled target pins and uses the supplied verified local runtime.
// The input grants no artifact or action authority. Normal Setup confirmation
// and permissions remain required. The ordinary Panel factory is unchanged.
func NewQualificationTransfer(machine *client.Machine, mirrors []QualificationMirror, expires time.Time) (PublicTransfer, error) {
	return qualificationTransfer(machine, NewCatalog(), mirrors, expires)
}

// NewInstalledQualificationTransfer admits transport only for the installed
// Catalog's desktop integration. The Panel verifies the installed runtime
// before using this constructor.
func NewInstalledQualificationTransfer(machine *client.Machine, mirrors []QualificationMirror, expires time.Time) (PublicTransfer, error) {
	return installedQualificationTransfer(machine, NewCatalog(), mirrors, expires)
}

func installedQualificationTransfer(machine *client.Machine, catalog Catalog, mirrors []QualificationMirror, expires time.Time) (PublicTransfer, error) {
	return qualificationTransfer(machine, Catalog{DesktopIntegration: catalog.DesktopIntegration}, mirrors, expires)
}

func qualificationTransfer(machine *client.Machine, catalog Catalog, mirrors []QualificationMirror, expires time.Time) (PublicTransfer, error) {
	if machine == nil || len(mirrors) == 0 || len(mirrors) > 1025 || !expires.After(time.Now()) || expires.After(time.Now().Add(30*time.Minute)) {
		return PublicTransfer{}, errors.New("qualification needs a verified local runtime, finite mirror set and expiry within thirty minutes")
	}
	allowed := map[qualificationPin]bool{}
	for _, component := range catalog.Components {
		if component.Artifact == nil || component.Artifact.NativeResources == nil {
			continue
		}
		a := *component.Artifact
		for i := 0; i <= len(a.NativeResources.Backend.Archive.Parts); i++ {
			pin, err := nativeResourceDownload(a, i)
			if err != nil || pin.validate() != nil {
				return PublicTransfer{}, errors.New("compiled native qualification pin is invalid")
			}
			allowed[qualificationPin{pin.URL, pin.SHA256, pin.Bytes}] = true
		}
	}
	if catalog.DesktopIntegration != nil {
		pin := catalog.DesktopIntegration.downloadPin()
		if err := pin.validate(); err != nil {
			return PublicTransfer{}, errors.New("compiled desktop integration qualification pin is invalid")
		}
		allowed[qualificationPin{pin.URL, pin.SHA256, pin.Bytes}] = true
	}
	q := &qualificationSources{expires: expires, mirrors: map[qualificationPin]string{}}
	attempt, endpoint := "", ""
	for _, mirror := range mirrors {
		key := qualificationPin{mirror.CanonicalURL, mirror.SHA256, mirror.Bytes}
		if !allowed[key] || q.mirrors[key] != "" {
			return PublicTransfer{}, errors.New("qualification mirror is duplicate or absent from compiled native pins")
		}
		u, err := url.Parse(mirror.MirrorURL)
		if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || u.Opaque != "" {
			return PublicTransfer{}, errors.New("qualification mirror needs an exact numeric loopback HTTP route")
		}
		host, port, err := net.SplitHostPort(u.Host)
		n, portErr := strconv.Atoi(port)
		parts := qualificationRoute.FindStringSubmatch(u.Path)
		if err != nil || host != "127.0.0.1" || portErr != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port || len(parts) != 3 || (attempt != "" && attempt != parts[1]) || (endpoint != "" && endpoint != u.Host) {
			return PublicTransfer{}, errors.New("qualification mirror route or attempt is invalid")
		}
		attempt = parts[1]
		endpoint = u.Host
		q.mirrors[key] = mirror.MirrorURL
	}
	return PublicTransfer{Machine: machine, LocalOnly: true, qualification: q}, nil
}

var qualificationRoute = regexp.MustCompile(`^/native-qualification/([0-9a-f]{32})/([0-9a-f]{64})$`)

func (t PublicTransfer) downloadSources(pin PinnedDownload, scheme string) ([]request.Source, error) {
	sources := []request.Source{{Scheme: scheme, Locator: pin.URL}}
	if t.qualification == nil {
		return sources, nil
	}
	if !time.Now().Before(t.qualification.expires) {
		return nil, errors.New("native qualification transport expired; reconcile the existing attempt")
	}
	if mirror := t.qualification.mirrors[qualificationPin{pin.URL, pin.SHA256, pin.Bytes}]; mirror != "" {
		return append([]request.Source{{Scheme: "http", Locator: mirror}}, sources...), nil
	}
	return sources, nil
}
