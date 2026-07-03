# Reasonix Plugin Packages

Reasonix plugin packages bundle skills, hooks, and MCP servers behind one
installable unit.

## CLI Mode

Use `reasonix plugin` when installing or managing plugin packages from a
terminal. Plugin packages are installed globally under the Reasonix home
directory.

### Install From CLI

`install` accepts one source:

- A GitHub repository, such as `git:github.com/obra/superpowers` or
  `https://github.com/obra/superpowers`.
- A GitHub branch or subdirectory URL, such as
  `https://github.com/owner/repo/tree/main/path/to/plugin`.
- A local directory that contains `reasonix-plugin.json` or
  `.codex-plugin/plugin.json`.

Preview the install plan without writing files:

```bash
reasonix plugin install git:github.com/obra/superpowers --dry-run
```

Install a plugin after reviewing the plan:

```bash
reasonix plugin install git:github.com/obra/superpowers --yes
```

Install with an explicit name or replace an installed plugin with the same name:

```bash
reasonix plugin install git:github.com/obra/superpowers --name superpowers --replace --yes
```

Use a local directory in developer mode:

```bash
reasonix plugin install /path/to/plugin --link --replace --yes
```

CLI install flags:

- `--dry-run` plans and validates the install without writing files.
- `--yes` is required for any install that writes files.
- `--replace` allows the source to replace an installed plugin with the same
  name.
- `--name <name>` or `--name=<name>` overrides the name from the plugin
  manifest for this install.
- `--link` links a local plugin directory instead of copying it into Reasonix's
  plugin storage. Moving or deleting that directory breaks the linked plugin.

Running `reasonix plugin install <source>` without `--dry-run` or `--yes`
refuses to write files and prints a reminder to rerun with one of those flags.
Install and remove commands print the structured JSON response from the same
install-source backend used by the desktop UI.

Installed plugin state is stored in:

```text
~/.reasonix/plugin-packages.json
~/.reasonix/plugins/<name>/
```

### Manage From CLI

List installed plugins:

```bash
reasonix plugin list
```

Show one plugin's metadata, root, source, and exported capability counts:

```bash
reasonix plugin show superpowers
```

Check that the manifest and skill roots are readable:

```bash
reasonix plugin doctor superpowers
```

Enable or disable a plugin without uninstalling it:

```bash
reasonix plugin disable superpowers
reasonix plugin enable superpowers
```

Remove a plugin:

```bash
reasonix plugin remove superpowers --yes
```

`remove` also accepts `uninstall` as an alias. It requires `--yes` because it
writes state and removes copied plugin content. For linked local plugins, the
external source directory is left in place.

## Desktop Settings

Open **Settings -> Plugins** to install and manage plugin packages without using
the CLI.

### Install Plugins

The installer has two modes:

- **Local folder**: click **Choose plugin folder** and select a plugin directory
  on disk. The selected path is shown next to the button.
- **Git repository**: enter a Git source such as
  `git:github.com/obra/superpowers`. **Install name (optional)** can override
  the plugin manifest name for this install or overwrite.

Use the action buttons after choosing the source and options:

- **Preview** validates the source and shows the planned install actions without
  writing files.
- **Install plugin** installs the selected source using the current options.
- **Refresh plugins** reloads the installed-plugin list from disk and config.

Installer options:

- **Overwrite same-name plugin** allows the current source to replace an
  installed plugin with the same name. Leave it off when duplicate-name installs
  should fail instead of replacing existing content.
- **Developer mode: link source folder** appears for **Local folder** installs.
  It links the selected directory instead of copying it into Reasonix's plugin
  storage. Use it while developing or debugging a plugin. Moving or deleting the
  selected directory will break the linked plugin.

Preview is the safest first step for a new Git source or local plugin directory.

### Manage Installed Plugins

The installed-plugin list shows each plugin package and its exported skills,
hooks, and MCP servers. Use **Refresh plugins** after editing plugin files or
changing config outside the app.

Expand a plugin row to manage it:

- Enable or disable the plugin.
- **Update** pulls or refreshes an installed plugin when an update source is
  available.
- **Doctor** checks the plugin manifest and reports warnings or diagnostics.
- **Remove plugin** uninstalls the package after confirmation.

## Native Manifest

Reasonix plugins can declare `reasonix-plugin.json` at the plugin root:

```json
{
  "name": "example",
  "version": "1.0.0",
  "description": "Example plugin",
  "skills": "skills",
  "hooks": {
    "SessionStart": [
      {
        "command": "hooks/session-start",
        "description": "Load startup context"
      }
    ]
  },
  "mcpServers": {
    "helper": {
      "command": "bin/helper"
    }
  }
}
```

Relative paths are resolved inside the plugin root. Reasonix does not run
third-party install scripts during plugin installation.

## Codex Compatibility

Reasonix also reads Codex plugin manifests at `.codex-plugin/plugin.json`.
For packages such as Superpowers, Reasonix maps:

- `skills` to Reasonix skill roots.
- `hooks/session-start-codex` to the Reasonix `SessionStart` hook when present.

Plugin hooks receive these environment variables:

- `REASONIX_PLUGIN_ROOT`
- `REASONIX_PLUGIN_NAME`
- `REASONIX_PLUGIN_VERSION`
- `REASONIX_HOME`
- `REASONIX_WORKSPACE_ROOT`

## Desktop Backend Methods

Desktop exposes plugin package operations through Wails methods:

- `Plugins`
- `PlanPluginInstall`
- `InstallPlugin`
- `RemovePlugin`
- `SetPluginEnabled`
- `UpdatePlugin`
- `PluginDoctor`
