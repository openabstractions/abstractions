package main

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
)

// givenInstall is an application's official program, its installed OA plugin
// and the config holding the OA provider, as the person or the adopter's own
// launcher named them (--program, --plugin, --config). The runtime keeps no
// list of where an application's installer puts its files: the adopter finds
// them (adopters/<app>/), and the runtime checks what it was given.
type givenInstall struct {
	Program, Plugin, PluginRoot, Config string
}

// checkGivenInstall checks each named path before any grant: an absolute
// regular program file, a plugin folder with its package.json and a regular config
// file. Nothing is downloaded or installed here.
func checkGivenInstall(program, plugin, config string, stat func(string) (fs.FileInfo, error)) (givenInstall, error) {
	if program == "" || plugin == "" || config == "" {
		return givenInstall{}, errors.New("--program, --plugin and --config are named together")
	}
	if !filepath.IsAbs(program) {
		return givenInstall{}, errors.New("--program requires the absolute path of the official program")
	}
	if err := givenRegularFile(program, stat); err != nil {
		return givenInstall{}, fmt.Errorf("--program %s: %w", program, err)
	}
	if !filepath.IsAbs(plugin) {
		return givenInstall{}, errors.New("--plugin requires an absolute folder")
	}
	if err := givenRegularFile(filepath.Join(plugin, "package.json"), stat); err != nil {
		return givenInstall{}, fmt.Errorf("--plugin %s: %w", plugin, err)
	}
	if !filepath.IsAbs(config) {
		return givenInstall{}, errors.New("--config requires an absolute file")
	}
	if err := givenRegularFile(config, stat); err != nil {
		return givenInstall{}, fmt.Errorf("--config %s: %w", config, err)
	}
	plugin = filepath.Clean(plugin)
	return givenInstall{Program: filepath.Clean(program), Plugin: plugin, PluginRoot: pluginPackageRoot(plugin), Config: filepath.Clean(config)}, nil
}

func givenRegularFile(path string, stat func(string) (fs.FileInfo, error)) error {
	info, err := stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	return nil
}

// pluginPackageRoot is the folder whose node_modules holds a scoped npm
// plugin's dependencies: <root> for <root>/node_modules/@scope/name, where an
// application's own npm install puts it, else the plugin folder itself (a
// checkout, or a build that bundles its plugin).
func pluginPackageRoot(plugin string) string {
	scope := filepath.Dir(plugin)
	modules := filepath.Dir(scope)
	if strings.HasPrefix(filepath.Base(scope), "@") && filepath.Base(modules) == "node_modules" {
		return filepath.Dir(modules)
	}
	return plugin
}
