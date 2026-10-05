package main

import (
	"fmt"
	"io"
	"slices"
	"strings"
)

type envVarJSON struct {
	Key     string `json:"key"`
	Value   string `json:"value,omitempty"` // left out for secrets
	Secret  bool   `json:"secret"`
	Set     bool   `json:"set"`
	Enabled bool   `json:"enabled"`
}

type envJSON struct {
	ID        string       `json:"id"`
	Name      string       `json:"name"`
	Active    bool         `json:"active"`
	Protected bool         `json:"protected"`
	Protects  string       `json:"protects,omitempty"` // "writes" or "all"
	Vars      []envVarJSON `json:"vars,omitempty"`
}

func (c *cli) envOut(env environment, withVars bool) envJSON {
	out := envJSON{ID: env.ID, Name: env.Name, Active: env.ID == c.ws.ActiveEnv, Protected: env.Protected, Protects: env.protection()}
	if withVars {
		out.Vars = []envVarJSON{}
		for _, v := range env.Vars {
			ev := envVarJSON{Key: v.Key, Secret: isSecret(v), Set: v.Value != "", Enabled: v.Enabled}
			if !ev.Secret || c.reveal {
				ev.Value = v.Value
			}
			out.Vars = append(out.Vars, ev)
		}
	}
	return out
}

func cmdEnv(c *cli, args []string) error {
	if len(args) == 0 {
		args = []string{"ls"}
	}
	sub, args := args[0], args[1:]
	c.command = "env " + sub
	fs := c.flags("env " + sub)

	switch sub {
	case "ls":
		if _, err := c.parse(fs, args, 0, 0, ""); err != nil {
			return err
		}
		if err := c.open(); err != nil {
			return err
		}
		envs := []envJSON{}
		for _, e := range c.ws.Environments {
			envs = append(envs, c.envOut(e, false))
		}
		if c.asJSON {
			return c.printJSON(envs)
		}
		if len(envs) == 0 {
			c.printf("No environments. Create one with `barq env new <name>`.\n")
		}
		for i, e := range envs {
			c.printf("%s%s  (%s, %d vars)\n", envFlags(e), e.Name, e.ID, len(c.ws.Environments[i].Vars))
		}
		return nil

	case "show":
		fs.BoolVar(&c.reveal, "reveal", false, "show secret values (needs a person at a terminal)")
		pos, err := c.parse(fs, args, 0, 1, "[environment]")
		if err != nil {
			return err
		}
		env, err := c.openEnv(pos, 0)
		if err != nil {
			return err
		}
		if err := c.checkReveal(); err != nil {
			return err
		}
		out := c.envOut(*env, true)
		if c.asJSON {
			return c.printJSON(out)
		}
		c.printf("%s%s  (%s)\n", envFlags(out), out.Name, out.ID)
		for _, v := range out.Vars {
			val := v.Value
			switch {
			case v.Secret && !c.reveal && v.Set:
				val = "«secret»"
			case v.Secret && !c.reveal:
				val = "«secret, not set»"
			}
			off := ""
			if !v.Enabled {
				off = "  (off)"
			}
			c.printf("  %s = %s%s\n", v.Key, val, off)
		}
		return nil

	case "use":
		pos, err := c.parse(fs, args, 1, 1, "<environment|none>")
		if err != nil {
			return err
		}
		if err := c.open(); err != nil {
			return err
		}
		id := ""
		if !strings.EqualFold(pos[0], "none") {
			env, err := c.ws.envByRef(pos[0])
			if err != nil {
				return err
			}
			id = env.ID
		}
		if err := c.mutate(func(w *workspace) error { return w.useEnv(id) }); err != nil {
			return err
		}
		return c.envDone(id, "using")

	case "new":
		protect := fs.Bool("protect", false, "require confirmation before the CLI sends requests that can change something")
		protectAll := fs.Bool("protect-all", false, "require confirmation before the CLI sends any request")
		use := fs.Bool("use", false, "make it the active environment")
		pos, err := c.parse(fs, args, 1, 1, "<name> [--protect | --protect-all] [--use]")
		if err != nil {
			return err
		}
		if err := c.open(); err != nil {
			return err
		}
		if _, err := c.ws.envByRef(pos[0]); err == nil {
			return fmt.Errorf("environment %q already exists", pos[0])
		}
		var id string
		if err := c.mutate(func(w *workspace) error {
			id = w.addEnv(pos[0], nil)
			e := &w.Environments[w.findEnv(id)]
			e.Protected, e.ProtectReads = *protect || *protectAll, *protectAll
			if *use || w.ActiveEnv == "" {
				w.ActiveEnv = id
			}
			return nil
		}); err != nil {
			return err
		}
		return c.envDone(id, "created")

	case "rm", "rename", "protect", "unprotect":
		want, usageText := 1, "<environment>"
		switch sub {
		case "rename":
			want, usageText = 2, "<environment> <new name>"
		case "protect":
			usageText = "<environment> [--all]"
		}
		all := new(bool)
		if sub == "protect" {
			all = fs.Bool("all", false, "confirm every request, not just ones that can change something")
		}
		pos, err := c.parse(fs, args, want, want, usageText)
		if err != nil {
			return err
		}
		env, err := c.openEnv(pos, 0)
		if err != nil {
			return err
		}
		id, name := env.ID, env.Name
		switch {
		case sub == "unprotect" && env.Protected:
			err = c.confirm(fmt.Sprintf("Unprotecting %q lets agents use it without asking", name))
		case sub == "protect" && env.ProtectReads && !*all:
			err = c.confirm(fmt.Sprintf("Protecting only writes in %q lets agents send GET, HEAD and OPTIONS there without asking", name))
		case sub == "rm" && env.Protected:
			err = c.confirm(fmt.Sprintf("Deleting the protected environment %q", name))
		}
		if err != nil {
			return err
		}
		if err := c.mutate(func(w *workspace) error {
			switch sub {
			case "rm":
				return w.deleteEnv(id)
			case "rename":
				return w.renameEnv(id, pos[1])
			}
			e, err := w.env(id)
			if err == nil {
				e.Protected = sub == "protect"
				e.ProtectReads = e.Protected && *all
			}
			return err
		}); err != nil {
			return err
		}
		if sub == "rm" {
			if c.asJSON {
				return c.printJSON(map[string]string{"deleted": name})
			}
			c.printf("deleted environment %s\n", name)
			return nil
		}
		if sub == "protect" {
			e, _ := c.ws.env(id)
			return c.envDone(id, "protected ("+e.protection()+")")
		}
		return c.envDone(id, sub+"d")

	case "set":
		secret := fs.Bool("secret", false, "mark the variable secret")
		notSecret := fs.Bool("no-secret", false, "mark the variable not secret")
		pos, err := c.parse(fs, args, 3, 3, "<environment> <key> <value|-> [--secret|--no-secret]   (- reads the value from stdin)")
		if err != nil {
			return err
		}
		env, err := c.openEnv(pos, 0)
		if err != nil {
			return err
		}
		key, value := pos[1], pos[2]
		if value == "-" {
			b, err := io.ReadAll(c.in)
			if err != nil {
				return err
			}
			value = strings.TrimRight(string(b), "\r\n")
		}
		if isRedacted(value) {
			return errRedactedWrite
		}
		i := slices.IndexFunc(env.Vars, func(v savedHeader) bool { return v.Key == key })
		current := savedHeader{Key: key}
		if i >= 0 {
			current = env.Vars[i]
		}
		if *notSecret && isSecret(current) {
			if err := c.confirm(fmt.Sprintf("Marking {{%s}} not secret makes its value visible to agents", key)); err != nil {
				return err
			}
		}
		id := env.ID
		if err := c.mutate(func(w *workspace) error {
			if err := w.setEnvVar(id, key, value); err != nil {
				return err
			}
			e, _ := w.env(id)
			v := &e.Vars[slices.IndexFunc(e.Vars, func(v savedHeader) bool { return v.Key == key })]
			switch {
			case *secret:
				v.Secret = boolPtr(true)
			case *notSecret:
				v.Secret = boolPtr(false)
			}
			return nil
		}); err != nil {
			return err
		}
		e, _ := c.ws.env(id)
		v := e.Vars[slices.IndexFunc(e.Vars, func(v savedHeader) bool { return v.Key == key })]
		if c.asJSON {
			out := envVarJSON{Key: key, Secret: isSecret(v), Set: v.Value != "", Enabled: true}
			if !out.Secret {
				out.Value = v.Value
			}
			return c.printJSON(out)
		}
		kind := ""
		if isSecret(v) {
			kind = " (secret)"
		}
		c.printf("set {{%s}}%s in %s\n", key, kind, e.Name)
		return nil

	case "unset":
		pos, err := c.parse(fs, args, 2, 2, "<environment> <key>")
		if err != nil {
			return err
		}
		env, err := c.openEnv(pos, 0)
		if err != nil {
			return err
		}
		id, key := env.ID, pos[1]
		if !slices.ContainsFunc(env.Vars, func(v savedHeader) bool { return v.Key == key }) {
			return fmt.Errorf("%s has no variable %q", env.Name, key)
		}
		if err := c.mutate(func(w *workspace) error {
			e, err := w.env(id)
			if err == nil {
				e.Vars = slices.DeleteFunc(e.Vars, func(v savedHeader) bool { return v.Key == key })
			}
			return err
		}); err != nil {
			return err
		}
		c.printf("removed {{%s}}\n", key)
		return nil
	}
	return usagef("unknown subcommand %q; use ls, show, use, new, rm, rename, set, unset, protect or unprotect", sub)
}

// openEnv opens the workspace and finds the environment named by
// pos[i], or the active one when it isn't given.
func (c *cli) openEnv(pos []string, i int) (*environment, error) {
	if err := c.open(); err != nil {
		return nil, err
	}
	if len(pos) > i {
		return c.ws.envByRef(pos[i])
	}
	if env := c.ws.activeEnv(); env != nil {
		return env, nil
	}
	return nil, fmt.Errorf("no active environment; name one (see `barq env ls`)")
}

func (c *cli) envDone(id, verb string) error {
	if id == "" {
		if c.asJSON {
			return c.printJSON(map[string]any{"active": nil})
		}
		c.printf("no environment\n")
		return nil
	}
	env, err := c.ws.env(id)
	if err != nil {
		return err
	}
	if c.asJSON {
		return c.printJSON(c.envOut(*env, false))
	}
	c.printf("%s %s\n", verb, env.Name)
	return nil
}

func envFlags(e envJSON) string {
	f := "  "
	if e.Active {
		f = "* "
	}
	if e.Protected {
		f += "🔒 "
	}
	return f
}
