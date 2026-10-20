package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// rustTarget is one declared Rust generation: the definition and the flags its
// generate.targets line passes to the generator.
type rustTarget struct {
	def   string
	out   string
	flags []string
}

func rustTargetOptions(words []string) (flags []string, rust bool) {
	var langs []string
	mcp := slices.Contains(words, "--mcp")
	for _, word := range words {
		if strings.HasPrefix(word, "-") {
			if (mcp || !strings.HasPrefix(word, "--go-")) && !strings.HasPrefix(word, "--js-") {
				flags = append(flags, word)
			}
		} else {
			langs = append(langs, word)
		}
	}
	rust = len(langs) == 0
	for _, lang := range langs {
		rust = rust || lang == "rust"
	}
	return flags, rust
}

// declaredRustTargets reads every Rust line of scripts/generate.targets. A
// public charter checkout has no targets file; it declares the maintained
// production inputs it publishes, each with the shared Rust transport.
func declaredRustTargets(t *testing.T) ([]rustTarget, bool) {
	t.Helper()
	root := filepath.Join("..", "..")
	f, err := os.Open(filepath.Join(root, "scripts", "generate.targets"))
	if os.IsNotExist(err) {
		var out []rustTarget
		for _, rel := range []string{"abstraction-job/acceptance.thrift", "abstraction-facade/facade.thrift", "abstraction-logging/logging.thrift"} {
			out = append(out, rustTarget{def: productionFile(t, rel), flags: []string{"--shared-rust-transport"}})
		}
		return out, false
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []rustTarget
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		words := strings.Fields(strings.SplitN(scan.Text(), "#", 2)[0])
		if len(words) < 2 {
			continue
		}
		flags, rust := rustTargetOptions(words[2:])
		if !rust {
			continue
		}
		def, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(words[0])))
		if err != nil {
			t.Fatal(err)
		}
		output, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(words[1])))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, rustTarget{def: def, out: output, flags: flags})
	}
	if err := scan.Err(); err != nil {
		t.Fatal(err)
	}
	return out, true
}

// rustCrates compiles generated Rust targets as separate crates, the way Cargo
// links generated API crates, reusing each compiled crate by name.
type rustCrates struct {
	t              *testing.T
	rust           string
	dir            string
	targets        []rustTarget
	built          map[string]string
	frame          string
	generated      int
	compiled       int
	generationTime time.Duration
	compilerTime   time.Duration
}

func (c *rustCrates) run(dir string, args ...string) {
	c.t.Helper()
	cmd := exec.Command(c.rust, args...)
	cmd.Dir = dir
	started := time.Now()
	if out, err := cmd.CombinedOutput(); err != nil {
		c.t.Fatalf("rustc %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	c.compilerTime += time.Since(started)
	c.compiled++
}

func (c *rustCrates) abstractionFrame() string {
	c.t.Helper()
	if c.frame == "" {
		core, err := filepath.Abs(productionFile(c.t, "abstraction-identity/rust-frame/src/lib.rs"))
		if err != nil {
			c.t.Fatal(err)
		}
		c.run(c.dir, "--edition=2021", "--crate-name=abstraction_frame", "--crate-type=rlib", core, "-o", "libabstraction_frame.rlib")
		c.frame = filepath.Join(c.dir, "libabstraction_frame.rlib")
	}
	return c.frame
}

// build generates target and compiles it as crate name, first building every
// crate its --rust-import mappings name from the target that declares the included definition.
func (c *rustCrates) build(target rustTarget, name string) string {
	c.t.Helper()
	if lib, ok := c.built[name]; ok {
		return lib
	}
	parts := strings.Split(name, "::")
	crateName := parts[0]
	fileName := strings.Join(parts, "_module_")
	def, err := loadDefinition(target.def)
	if err != nil {
		c.t.Fatal(target.def, err)
	}
	externs := []string{}
	for _, fl := range target.flags {
		if strings.HasPrefix(fl, "--shared-rust-transport") {
			externs = append(externs, "--extern", "abstraction_frame="+c.abstractionFrame())
		}
		mapping, ok := strings.CutPrefix(fl, "--rust-import=")
		if !ok {
			continue
		}
		alias, crate, _ := strings.Cut(mapping, "=")
		var included string
		for _, imp := range def.Imports {
			if imp.Alias == alias {
				included = filepath.Clean(filepath.Join(filepath.Dir(target.def), filepath.FromSlash(imp.Path)))
			}
		}
		dependency := -1
		for i, other := range c.targets {
			if filepath.Clean(other.def) == included {
				dependency = i
			}
		}
		if dependency < 0 {
			c.t.Fatalf("%s maps include %s to %s, and no declared Rust target generates %s", target.def, alias, crate, included)
		}
		externs = append(externs, "--extern", strings.Split(crate, "::")[0]+"="+c.build(c.targets[dependency], crate))
	}
	out := filepath.Join(c.dir, fileName)
	var report bytes.Buffer
	started := time.Now()
	args := append(append([]string{target.def, out}, target.flags...), "rust")
	// MCP validation needs native Go codecs even though rustc only reads Rust.
	if slices.Contains(target.flags, "--mcp") {
		args = append(args, "go")
	}
	if err := run(args, &report); err != nil {
		c.t.Fatalf("generate %s: %v", target.def, err)
	}
	c.generationTime += time.Since(started)
	c.generated++
	var source string
	filepath.Walk(filepath.Join(out, "rs"), func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && info.Name() == "rec.rs" {
			source = p
		}
		return nil
	})
	if source == "" {
		c.t.Fatalf("%s generated no Rust source", target.def)
	}
	if target.out != "" {
		// Compile the real declared projection and prove the emitter preserves
		// the frozen source bytes, including selected/namespace profiles.
		relative, err := filepath.Rel(out, source)
		if err != nil {
			c.t.Fatal(err)
		}
		expected, err := os.ReadFile(filepath.Join(target.out, relative))
		if err != nil {
			c.t.Fatal(err)
		}
		actual, err := os.ReadFile(source)
		if err != nil {
			c.t.Fatal(err)
		}
		if !bytes.Equal(actual, expected) {
			c.t.Fatalf("%s %v: emitted Rust differs from declared artifact %s", target.def, target.flags, filepath.Join(target.out, relative))
		}
	}
	if len(parts) > 1 {
		// The existing include mapping accepts a crate's canonical module,
		// such as abstraction_facade_service::wire. Build that same module
		// placement rather than treating :: as a path or crate identifier.
		wrapper := fmt.Sprintf("#[path = r#%q#] pub mod %s;\n", filepath.ToSlash(source), parts[len(parts)-1])
		for i := len(parts) - 2; i > 0; i-- {
			wrapper = fmt.Sprintf("pub mod %s {\n%s}\n", parts[i], wrapper)
		}
		source = filepath.Join(out, "module_root.rs")
		if err := os.WriteFile(source, []byte(wrapper), 0600); err != nil {
			c.t.Fatal(err)
		}
	}
	lib := filepath.Join(c.dir, "lib"+fileName+".rlib")
	// Warnings are denied and the naming lints forbidden, so neither dead code
	// nor an #[allow] suppression can ship in a generated crate.
	c.run(c.dir, append([]string{"--edition=2021", "--crate-type=rlib", "--crate-name=" + crateName, "-D", "warnings", "-F", "nonstandard_style", source, "-o", lib, "-L", c.dir}, externs...)...)
	c.built[name] = lib
	return lib
}

// TestRustEveryDeclaredTargetCompiles compiles each declared Rust generation
// with its declared flags. Generator or validation gaps that produce
// uncompilable Rust fail here before any regeneration reaches a crate build.
func TestRustEveryDeclaredTargetCompiles(t *testing.T) {
	rust := rustServiceCompiler(t)
	targets, private := declaredRustTargets(t)
	if len(targets) == 0 {
		t.Fatal("no declared Rust targets")
	}
	crates := &rustCrates{t: t, rust: rust, dir: t.TempDir(), targets: targets, built: map[string]string{}}
	compiled := 0
	for i, target := range targets {
		name := fmt.Sprintf("declared_target_%d", i)
		t.Run(filepath.Base(filepath.Dir(target.def))+"/"+filepath.Base(target.def), func(t *testing.T) {
			crates.t = t
			crates.build(target, name)
			compiled++
		})
	}
	t.Logf("compiled %d of %d declared Rust targets (targets file present: %v)", compiled, len(targets), private)
	t.Logf("generation: %d projections in %s; rustc: %d independent crates in %s", crates.generated, crates.generationTime, crates.compiled, crates.compilerTime)
}

func TestRustDeclaredTargetFlagsPreserveSelectedSurface(t *testing.T) {
	flags, rust := rustTargetOptions([]string{"python", "cpp", "rust", "-only=Code,Limits", "--namespace=rust=abstraction.ipc.constants", "--shared-rust-transport", "--go-import=a=b", "--js-import=a=c"})
	want := "-only=Code,Limits --namespace=rust=abstraction.ipc.constants --shared-rust-transport"
	if !rust || strings.Join(flags, " ") != want {
		t.Fatalf("rust=%v flags=%v", rust, flags)
	}
}

// TestRustKeywordArgumentFailsBeforeCrate is the control for the gap class the
// declared-target compile exists to catch: a keyword service argument must fail
// in the generator, before a crate build.
func TestRustKeywordArgumentFailsBeforeCrate(t *testing.T) {
	dir := t.TempDir()
	writeNamespaceFile(t, dir, "abstraction-download/request.thrift", readProduction(t, "abstraction-download/request.thrift"))
	writeNamespaceFile(t, dir, "abstraction-storage/content.thrift", readProduction(t, "abstraction-storage/content.thrift"))
	model := readProduction(t, "abstraction-model/model.thrift")
	stripped := strings.Replace(model, `Ref ref (rust.name = "reference")`, "Ref ref", 1)
	if stripped == model {
		t.Skip("production model no longer carries the renamed keyword argument")
	}
	writeNamespaceFile(t, dir, "abstraction-model/model.thrift", stripped)
	var report bytes.Buffer
	err := run([]string{filepath.Join(dir, "abstraction-model", "model.thrift"), filepath.Join(dir, "out"), "--rust-import=request=abstraction_download_request_api", "--rust-import=content=abstraction_storage_content_api", "--shared-rust-transport", "rust"}, &report)
	if err == nil || !strings.Contains(err.Error(), "rust.name") {
		t.Fatalf("keyword argument reached Rust output: %v", err)
	}
	if _, e := os.Stat(filepath.Join(dir, "out")); !os.IsNotExist(e) {
		t.Fatal("refused generation wrote output")
	}
}

func readProduction(t *testing.T, rel string) string {
	t.Helper()
	path := productionFile(t, rel)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
