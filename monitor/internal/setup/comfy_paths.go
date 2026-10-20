package setup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	facade "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/programfiles"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-storage/go/layout"
)

const (
	comfyPortableDirectory  = "ComfyUI_windows_portable"
	comfyPythonRelativePath = comfyPortableDirectory + "/python_embeded/python.exe"
	comfyLauncherName       = "oa_comfyui_launch.py"
)

// ComfyPaths contains paths derived from the current approved setup journal and
// an intact, catalog-pinned ComfyUI install receipt. Obtain it for setup or an
// explicit launch; opening it rehashes the installed archive tree.
type ComfyPaths struct {
	install string
	caller  string
	native  string
	user    string
	input   string
	temp    string
	output  string
	recipe  string
	// records retains the historical package's RECORD-set receipt pin.
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
		return ComfyPaths{}, errors.New(nativeArchiveAppTitle + " setup plan is not approved")
	}
	// During an update the retained earlier version stays the active one until
	// the new version is checked and activated.
	artifact, err := journal.knownInstalledArtifact(state, retainedArchiveComponent)
	if err != nil {
		return ComfyPaths{}, fmt.Errorf("%s app pin: %w", nativeArchiveAppTitle, err)
	}
	root, err := installPath(artifact)
	if err != nil {
		return ComfyPaths{}, err
	}
	installed, err := archiveInstalledContext(ctx, artifact, root)
	if err != nil {
		return ComfyPaths{}, fmt.Errorf("%s install receipt: %w", nativeArchiveAppTitle, err)
	}
	if !installed {
		return ComfyPaths{}, errors.New(nativeArchiveAppTitle + " install receipt or installed files no longer match the approved package")
	}
	paths, err := comfyPathsForArtifact(artifact)
	if err != nil {
		return ComfyPaths{}, err
	}
	for _, program := range []string{paths.caller, paths.Program()} {
		info, err := os.Lstat(program)
		if err != nil {
			return ComfyPaths{}, fmt.Errorf("%s installed program: %w", nativeArchiveAppTitle, err)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return ComfyPaths{}, errors.New(nativeArchiveAppTitle + " program is not a regular installed file")
		}
	}
	if artifact.NativeResources != nil {
		user, err := nativeArchiveUserDirectory(ctx, artifact, root)
		if err != nil {
			return ComfyPaths{}, err
		}
		paths.user = user
		paths.recipe = filepath.Join(user, filepath.FromSlash(oaArchiveLayout.ImageRecipe))
	}
	return paths, nil
}

// comfyPathsForArtifact derives the fixed paths of one pinned ComfyUI version
// without reading its files. Callers that launch must verify the receipt.
func comfyPathsForArtifact(artifact Artifact) (ComfyPaths, error) {
	root, caller, native, err := archiveProgramPaths(artifact, comfyPythonRelativePath, nativeArchiveAppTitle)
	if err != nil {
		return ComfyPaths{}, err
	}
	home, err := CurrentHome()
	if err != nil {
		return ComfyPaths{}, fmt.Errorf("ComfyUI local app data: %w", err)
	}
	localData, pictures := home.Data, home.Pictures
	if pictures == "" {
		if pictures, err = layout.ResolvePerson(layout.Current(), layout.Pictures); err != nil {
			return ComfyPaths{}, fmt.Errorf("ComfyUI Pictures directory: %w", err)
		}
	}
	paths, err := comfyPathsAt(root, caller, localData, pictures)
	if err != nil {
		return ComfyPaths{}, err
	}
	paths.records = artifact.RecordsSHA256
	paths.native = native
	return paths, nil
}

// EarlierComfyDescriptors returns receipt-derived display records for retained
// versions. The registration owner preserves foreign entries.
func EarlierComfyDescriptors(ctx context.Context, journal Journal) ([]facade.ApplicationDescriptor, error) {
	state, err := journal.Load(ctx)
	if err != nil {
		return nil, err
	}
	effect := state.Effects[retainedArchiveComponent]
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

// InstalledComfyProgram names the program the installed ComfyUI's
// registration names, from the journal alone: the version in use and its
// fixed member path. It reads no installed file and confers no authority to
// launch or register; those open the receipt (OpenInstalledComfy). Setup's
// install state compares it with the registered entry.
func InstalledComfyProgram(ctx context.Context, journal Journal) (string, error) {
	state, err := journal.Load(ctx)
	if err != nil {
		return "", err
	}
	if state.ApprovedPlanID == "" || state.ApprovedPlanID != state.Plan.ID {
		return "", errors.New("ComfyUI setup plan is not approved")
	}
	artifact, err := activeArtifact(state, retainedArchiveComponent)
	if err != nil {
		return "", err
	}
	paths, err := comfyPathsForArtifact(artifact)
	if err != nil {
		return "", err
	}
	return paths.Program(), nil
}

// ComfyInstallFolderHolds reports whether a program lies below a native
// or portable archive root where Setup installs versioned packages.
func ComfyInstallFolderHolds(program string) bool {
	return archiveRootsHold(program, oaArchiveLayout.NativeRoot, oaArchiveLayout.PortableRoot)
}

// ComfyNativeRootHolds reports whether a program lies below the native
// archive root. Setup only re-pins installed versions within that root.
func ComfyNativeRootHolds(program string) bool {
	return archiveRootsHold(program, oaArchiveLayout.NativeRoot)
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

// comfyDataRoot matches the host and node pack's shared mutable directory
// beneath either the account's known folders or Setup's isolated home.
func comfyDataRoot(base string) string {
	return filepath.Join(base, filepath.FromSlash(oaArchiveLayout.DataRoot))
}

// nativeSealRecord matches the seal-backend command's record for the pinned
// backend manifest under the host's mutable data directory.
func nativeSealRecord(a Artifact) (string, error) {
	home, err := CurrentHome()
	if err != nil {
		return "", err
	}
	sha := a.NativeResources.Backend.Provenance.Manifest.SHA256
	if !sha256Hex.MatchString(sha) {
		return "", errors.New("invalid native manifest pin")
	}
	return filepath.Join(comfyDataRoot(home.Data), "native", sha, programfiles.NativeTreeRecordName), nil
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
	dataRoot := comfyDataRoot(localData)
	user := filepath.Join(dataRoot, oaArchiveLayout.User)
	return ComfyPaths{
		install: installRoot,
		caller:  caller,
		user:    user,
		input:   filepath.Join(dataRoot, oaArchiveLayout.Input),
		temp:    filepath.Join(dataRoot, oaArchiveLayout.Temp),
		output:  comfyDataRoot(pictures),
		recipe:  filepath.Join(user, filepath.FromSlash(oaArchiveLayout.ImageRecipe)),
	}, nil
}

// withinPath reports whether target lies inside root (identity.Inside: by file
// identity when root exists, by exact spelling when it does not yet, and
// false when identity refuses either path).
func withinPath(root, target string) bool { return identity.Inside(target, root) }

// Program is the receipt-bound program OpenAbstractions records when this
// application calls it, and so the one program its registration and its
// granted rules name. In the native package that is the host: it starts the
// application with a relay variable naming its own image, and the
// application's relay (oa_relay.py) runs every OA call through
// "<host> native-relay". The venv interpreter the package also carries makes
// none of those calls. A retained earlier portable version names the
// interpreter its receipt binds.
func (p ComfyPaths) Program() string {
	if p.native != "" {
		return p.native
	}
	return p.caller
}

// UserDirectory returns ComfyUI's stable per-user data directory.
func (p ComfyPaths) UserDirectory() string { return p.user }

// InputDirectory returns ComfyUI's stable per-user input directory.
func (p ComfyPaths) InputDirectory() string { return p.input }

// TempDirectory returns ComfyUI's stable per-user temporary directory.
func (p ComfyPaths) TempDirectory() string { return p.temp }

// OutputDirectory returns the application output directory under Pictures.
func (p ComfyPaths) OutputDirectory() string { return p.output }

// PrepareDataDirectories creates only the fixed app-owned directories used by
// ComfyUI. It never writes into the immutable versioned application tree.
func (p ComfyPaths) PrepareDataDirectories() error {
	directories := []string{p.user, p.input, p.temp, p.output, filepath.Dir(p.recipe)}
	if p.native != "" {
		directories = []string{p.user, filepath.Dir(p.recipe)}
	}
	return prepareArchiveDataDirectories(directories, nativeArchiveAppTitle)
}

// WriteImagePolicy saves the pinned recipe after the caller confirms selection.
func (p ComfyPaths) WriteImagePolicy() error {
	return writeArchiveImageRecipe(p.recipe, nativeArchiveAppTitle, oaArchiveLayout.ImagePolicy, p.PrepareDataDirectories)
}

// VerifyImagePolicy validates the exact versioned recipe written by Setup.
func (p ComfyPaths) VerifyImagePolicy() error {
	return verifyArchiveImageRecipe(p.recipe, nativeArchiveAppTitle, oaArchiveLayout.ImagePolicy)
}
