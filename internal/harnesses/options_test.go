package harnesses

import (
	"context"
	"reflect"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/config"
)

// This file answers one question per option of a program that runs in a
// terminal: every setting the dashboard has is a promise that setting it
// changes what the program is started with, and a promise nothing checks is a
// promise that quietly stops being true.
//
// The tables are kept honest by reflection: a field of the option struct with
// no entry here fails the test, so an option added to the catalogue cannot be
// added without saying where it goes. The coding agents' options reach their
// chats instead, and the agent host's tests pin those.

// optionCase is one option, what it is set to, and what that must produce.
type optionCase struct {
	// field is the name of the field in the option struct.
	field string
	// set puts the option into its interesting state.
	set func(o *config.HarnessSettings)
	// want is what the launch plan must then carry. A nil want means the
	// option deliberately does not reach a launch plan at all, and why is in
	// the note.
	want func(t *testing.T, p LaunchPlan)
	note string
}

// runOptionCases builds one plan per case and checks it, then reports any
// field of the option struct the table forgot.
func runOptionCases(t *testing.T, h Terminal, options any, cases []optionCase) {
	t.Helper()
	covered := map[string]bool{}
	for _, c := range cases {
		covered[c.field] = true
		t.Run(c.field, func(t *testing.T) {
			l := newLab(t)
			c.set(&l.settings.Harnesses)
			plan, err := h.Plan(context.Background(), l.req())
			if err != nil {
				t.Fatalf("planning with %s set: %v", c.field, err)
			}
			if c.want == nil {
				if c.note == "" {
					t.Fatalf("%s has no expectation and no reason", c.field)
				}
				return
			}
			c.want(t, plan)
		})
	}
	for _, name := range fieldNames(reflect.TypeOf(options)) {
		if !covered[name] {
			t.Errorf("%T.%s is in the option catalogue and in no case here: either it reaches a launch plan and this table must say how, or it does not and the table must say why", options, name)
		}
	}
}

// fieldNames flattens an option struct, walking into the embedded Common.
func fieldNames(typ reflect.Type) []string {
	var out []string
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.Anonymous && field.Type.Kind() == reflect.Struct {
			out = append(out, fieldNames(field.Type)...)
			continue
		}
		out = append(out, field.Name)
	}
	return out
}

func TestShellOptionsReachTheLaunch(t *testing.T) {
	runOptionCases(t, Shell{}, config.ShellOptions{}, []optionCase{
		{field: "Enabled", note: "whether the picker offers the harness at all, which is the catalogue's business and not the launcher's",
			set: func(o *config.HarnessSettings) { o.Shell.Enabled = false }},
		{field: "Binary", note: "an override is resolved to argv[0]; TestEnvAlwaysCarriesWhite pins that argv[0] is the resolved absolute path",
			set: func(o *config.HarnessSettings) { o.Shell.Binary = "/bin/sh" }},
		{field: "Models", note: "a shell has no model step",
			set: func(o *config.HarnessSettings) { o.Shell.Models = []config.ModelPick{{ID: "x"}} }},
	})
}
