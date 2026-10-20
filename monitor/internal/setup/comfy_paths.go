package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	facade "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	identity "github.com/openabstractions/abstraction-identity"
)

const comfyImagePolicy = "oa-image-default"

const (
	comfyPortableDirectory  = "ComfyUI_windows_portable"
	comfyPythonRelativePath = comfyPortableDirectory + "/python_embeded/python.exe"
)

type comfyImageRecipe struct {
	Version         int    `json:"version"`
	SelectionPolicy string `json:"selection_policy"`
}

// ComfyPaths contains paths derived from the current approved setup journal and
// an intact, catalog-pinned ComfyUI install receipt. Obtain it for setup or an
// explicit launch; opening it rehashes the installed archive tree.
type ComfyPaths struct {
	install string
	caller  string
	user    string
	input   string
	temp    string
	output  string
	recipe  string
	// launch holds the launcher's record of its last full RECORD check.
	launch string
	// records is the reviewed RECORD-set SHA-256 of the installed package's
	// private Python, carried by its catalog Artifact as RecordsSHA256. The
	// activation recipe passes it to the launcher as --records, so a
	// distribution planted with its own RECORD refuses the launch.
	records string
}

// OpenInstalledComfy resolves the embedded Python only after the exact
// ComfyUI catalog artifact and its install receipt have been revalidated.
// Call this for a bounded setup or launch action, never from a polling path.
func OpenInstalledComfy(ctx context.Context, journal Journal) (ComfyPaths, error) {
	state, err := journal.Load(ctx)
	if err != nil {
		return ComfyPaths{}, err
	}
	if err := ctx.Err(); err != nil {
		return ComfyPaths{}, err
	}
	if state.ApprovedPlanID == "" || state.ApprovedPlanID != state.Plan.ID {
		return ComfyPaths{}, errors.New("ComfyUI setup plan is not approved")
	}
	// During an update the retained earlier version stays the active one until
	// the new version is checked and activated.
	artifact, err := journal.knownInstalledArtifact(state, "comfyui-app")
	if err != nil {
		return ComfyPaths{}, fmt.Errorf("ComfyUI app pin: %w", err)
	}
	root, err := installPath(artifact)
	if err != nil {
		return ComfyPaths{}, err
	}
	installed, err := ArchiveInstalled(artifact, root)
	if err != nil {
		return ComfyPaths{}, fmt.Errorf("ComfyUI install receipt: %w", err)
	}
	if !installed {
		return ComfyPaths{}, errors.New("ComfyUI install receipt or installed files no longer match the approved package")
	}
	python := filepath.Join(root, filepath.FromSlash(comfyPythonRelativePath))
	info, err := os.Lstat(python)
	if err != nil {
		return ComfyPaths{}, fmt.Errorf("ComfyUI embedded Python: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return ComfyPaths{}, errors.New("ComfyUI embedded Python is not a regular installed file")
	}
	return comfyPathsForArtifact(artifact)
}

// comfyPathsForArtifact derives the fixed paths of one pinned ComfyUI version
// without reading its files. Callers that launch must verify the receipt.
func comfyPathsForArtifact(artifact Artifact) (ComfyPaths, error) {
	if artifact.Platform != "windows/amd64" || artifact.Action != "archive_per_user" {
		return ComfyPaths{}, errors.New("ComfyUI app is not a Windows x64 portable archive")
	}
	root, err := installPath(artifact)
	if err != nil {
		return ComfyPaths{}, err
	}
	python := filepath.Join(root, filepath.FromSlash(comfyPythonRelativePath))
	home, err := CurrentHome()
	if err != nil {
		return ComfyPaths{}, fmt.Errorf("ComfyUI local app data: %w", err)
	}
	localData, pictures := home.Data, home.Pictures
	if pictures == "" {
		if pictures, err = comfyPicturesDirectory(); err != nil {
			return ComfyPaths{}, fmt.Errorf("ComfyUI Pictures directory: %w", err)
		}
	}
	paths, err := comfyPathsAt(root, identity.CanonicalProgramPath(filepath.Clean(python)), localData, pictures)
	if err != nil {
		return ComfyPaths{}, err
	}
	paths.records = artifact.RecordsSHA256
	return paths, nil
}

// EarlierComfyDescriptors returns the activation descriptors this Setup
// built for ComfyUI versions an update replaced or is replacing. A registered
// entry equal to one of them is this Setup's own earlier registration, which
// the updated version's registration may replace; any other entry is
// preserved.
func EarlierComfyDescriptors(ctx context.Context, journal Journal) ([]facade.ApplicationDescriptor, error) {
	state, err := journal.Load(ctx)
	if err != nil {
		return nil, err
	}
	effect := state.Effects["comfyui-app"]
	earlier := slices.Clone(effect.Retired)
	if effect.Retained != nil {
		earlier = append(earlier, *effect.Retained)
	}
	var descriptors []facade.ApplicationDescriptor
	for _, artifact := range earlier {
		paths, err := comfyPathsForArtifact(artifact)
		if err != nil {
			continue
		}
		if descriptor, err := ComfyApplicationDescriptor(paths); err == nil {
			descriptors = append(descriptors, descriptor)
		}
	}
	return descriptors, nil
}

// ProgramWithin reports whether a registered program path lies inside an
// installed version's folder, ignoring case as Windows paths do. The Panel's
// retire guard keeps a version a registered entry still names, so a path
// identity refuses (LocalPath), which cannot be shown outside, counts as
// within and the version is kept.
func ProgramWithin(root, program string) bool {
	root, program = filepath.Clean(root), filepath.Clean(program)
	if identity.LocalPath(root) != nil || identity.LocalPath(program) != nil {
		return true
	}
	return withinPath(root, program)
}

func comfyPathsAt(installRoot, caller, localData, pictures string) (ComfyPaths, error) {
	for _, item := range [][2]string{{"install root", installRoot}, {"caller", caller},
		{"local app data", localData}, {"Pictures", pictures}} {
		label, value := item[0], item[1]
		if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value {
			return ComfyPaths{}, fmt.Errorf("ComfyUI %s path must be absolute and clean", label)
		}
	}
	if !withinPath(installRoot, caller) {
		return ComfyPaths{}, errors.New("ComfyUI caller is outside the receipt-bound installation")
	}
	dataRoot := filepath.Join(localData, "OpenAbstractions", "ComfyUI")
	user := filepath.Join(dataRoot, "user")
	return ComfyPaths{
		install: installRoot,
		caller:  caller,
		user:    user,
		input:   filepath.Join(dataRoot, "input"),
		temp:    filepath.Join(dataRoot, "temp"),
		output:  filepath.Join(pictures, "OpenAbstractions", "ComfyUI"),
		recipe:  filepath.Join(user, "openabstractions", "image-recipe.json"),
		launch:  filepath.Join(dataRoot, "launch"),
	}, nil
}

// withinPath reports whether target lies inside root (identity.Inside: by file
// identity when root exists, by exact spelling when it does not yet, and
// false when identity refuses either path).
func withinPath(root, target string) bool { return identity.Inside(target, root) }

// CallerProgram returns the receipt-bound embedded Python executable used as
// the target program for caller-scoped public setup operations.
func (p ComfyPaths) CallerProgram() string { return p.caller }

// UserDirectory returns ComfyUI's stable per-user data directory.
func (p ComfyPaths) UserDirectory() string { return p.user }

// InputDirectory returns ComfyUI's stable per-user input directory.
func (p ComfyPaths) InputDirectory() string { return p.input }

// TempDirectory returns ComfyUI's stable per-user temporary directory.
func (p ComfyPaths) TempDirectory() string { return p.temp }

// OutputDirectory returns the application output directory under Pictures.
func (p ComfyPaths) OutputDirectory() string { return p.output }

// LaunchStateDirectory returns where the ComfyUI launcher keeps the record of
// its last full RECORD check. ComfyUI's own HTTP API serves none of it.
func (p ComfyPaths) LaunchStateDirectory() string { return p.launch }

// PrepareDataDirectories creates only the fixed app-owned directories used by
// ComfyUI. It never writes into the immutable versioned application tree.
func (p ComfyPaths) PrepareDataDirectories() error {
	for _, path := range []string{p.user, p.input, p.temp, p.output, p.launch, filepath.Dir(p.recipe)} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("ComfyUI data path is not a regular directory: %s", path)
		}
	}
	return nil
}

// WriteImagePolicy atomically saves the one fixed policy name after setup has
// successfully created and publicly confirmed its caller-scoped selection.
func (p ComfyPaths) WriteImagePolicy() error {
	if _, err := os.Lstat(p.recipe); err == nil {
		if err := p.VerifyImagePolicy(); err == nil {
			return nil
		}
		return errors.New("an existing ComfyUI image recipe differs from OA setup; its contents were preserved")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := p.PrepareDataDirectories(); err != nil {
		return err
	}
	data, err := json.Marshal(comfyImageRecipe{Version: 1, SelectionPolicy: comfyImagePolicy})
	if err != nil {
		return err
	}
	data = append(data, '\n')
	parent := filepath.Dir(p.recipe)
	temporary, err := os.CreateTemp(parent, ".image-recipe-*.tmp")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	// Linking publishes a complete file atomically and fails if another setup
	// action or the user created the destination after the initial check.
	if err := os.Link(temporaryName, p.recipe); err != nil {
		if errors.Is(err, os.ErrExist) && p.VerifyImagePolicy() == nil {
			return nil
		}
		return err
	}
	return os.Remove(temporaryName)
}

// VerifyImagePolicy validates the exact versioned recipe written by setup.
// Extra keys and alternate policy names are rejected.
func (p ComfyPaths) VerifyImagePolicy() error {
	info, err := os.Lstat(p.recipe)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 4096 {
		return errors.New("ComfyUI image recipe is not a small regular file")
	}
	data, err := os.ReadFile(p.recipe)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var recipe comfyImageRecipe
	if err := decoder.Decode(&recipe); err != nil {
		return fmt.Errorf("ComfyUI image recipe is invalid: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("ComfyUI image recipe contains trailing data")
	}
	if recipe.Version != 1 || recipe.SelectionPolicy != comfyImagePolicy {
		return errors.New("ComfyUI image recipe does not select the configured OA image policy")
	}
	return nil
}
