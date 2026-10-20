package nativecli

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/openabstractions/abstraction-facade/go/runtime/packaged"
	identity "github.com/openabstractions/abstraction-identity"
	"golang.org/x/sys/windows"
)

var (
	claudeAppID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

func claudeProcessPackage(process windows.Handle) (string, error) {
	return packaged.ProcessFamily(process)
}
func registeredClaudePackages(ctx context.Context) ([]string, error) {
	return registeredProductPackages(ctx, claudeProductFamily)
}
func registeredProductPackages(ctx context.Context, family string) ([]string, error) {
	rows, err := packaged.Registrations(ctx, family)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		names = append(names, row.Name)
	}
	return names, nil
}
func claudeRegisteredPath(name string) (string, error) {
	family, ok := identity.PackageFamily(name)
	if !ok {
		return "", errors.New("registered product identity unavailable")
	}
	rows, err := packaged.Registrations(context.Background(), family)
	if err != nil {
		return "", err
	}
	for _, row := range rows {
		if row.Name == name {
			return row.Path, nil
		}
	}
	return "", errors.New("registered product changed; retry")
}

func claudeManifestApplication(input io.Reader) (string, error) {
	decoder := xml.NewDecoder(io.LimitReader(input, 262145))
	var path []string
	var found []string
	tokens := 0
	roots := 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		tokens++
		if tokens > 8192 || decoder.InputOffset() > 262144 {
			return "", errors.New("registered product manifest exceeds its bound")
		}
		switch value := token.(type) {
		case xml.StartElement:
			if len(path) == 0 {
				roots++
				if roots != 1 || value.Name.Local != "Package" {
					return "", errors.New("invalid registered product manifest root")
				}
			}
			path = append(path, value.Name.Local)
			if len(path) > 32 {
				return "", errors.New("registered product manifest is too deep")
			}
			if strings.Join(path, "/") == "Package/Applications/Application" {
				id := ""
				seen := false
				for _, attribute := range value.Attr {
					if attribute.Name.Local == "Id" {
						if seen {
							return "", errors.New("duplicate registered application identity")
						}
						id = attribute.Value
						seen = true
					}
				}
				if !claudeAppID.MatchString(id) {
					return "", errors.New("invalid registered application identity")
				}
				found = append(found, id)
			}
		case xml.EndElement:
			path = path[:len(path)-1]
		case xml.Directive:
			return "", errors.New("registered product manifest directive refused")
		}
	}
	if len(found) != 1 {
		return "", errors.New("registered product application is absent or ambiguous")
	}
	return found[0], nil
}

func selectedClaudeActivation(ctx context.Context) (string, error) {
	names, err := registeredClaudePackages(ctx)
	if err != nil {
		return "", err
	}
	if len(names) != 1 {
		return "", errors.New("registered product is absent or ambiguous")
	}
	root, err := claudeRegisteredPath(names[0])
	if err != nil {
		return "", err
	}
	manifest, err := openArchiveSource(ctx, filepath.Join(root, "AppxManifest.xml"))
	if err != nil {
		return "", err
	}
	defer manifest.Close()
	id, err := claudeManifestApplication(manifest)
	if err != nil {
		return "", err
	}
	current, err := registeredClaudePackages(ctx)
	if err != nil || len(current) != 1 || current[0] != names[0] {
		return "", errors.New("registered product changed; retry")
	}
	return claudeProductFamily + "!" + id, ctx.Err()
}

// OpenClaudeCode activates the current registered desktop product with empty
// arguments. It does not create a Code session or launch the selected CLI.
func OpenClaudeCode(ctx context.Context) error {
	return openClaudeProduct(ctx, selectedClaudeActivation, activateClaudeProduct)
}

func openClaudeProduct(ctx context.Context, selectApp func(context.Context) (string, error), activate func(string) error) error {
	app, err := selectApp(ctx)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(app, claudeProductFamily+"!") || !claudeAppID.MatchString(strings.TrimPrefix(app, claudeProductFamily+"!")) {
		return errors.New("registered product activation identity refused")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return activate(app)
}

func activateClaudeProduct(app string) error {
	_, err := packaged.ActivateApplication(context.Background(), app)
	return err
}
