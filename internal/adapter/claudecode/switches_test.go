package claudecode

import (
	"strings"
	"testing"
)

var allSwitches = []Switches{{}, {RemindOff: true}, {CheckOff: true}, {RemindOff: true, CheckOff: true}}

func TestCommandStampRoundTrip(t *testing.T) {
	for _, v := range []string{"v1.2.0", "devel"} {
		for _, sw := range allSwitches {
			gv, gsw, ok := handler{Command: command(v, sw)}.stamp()
			if !ok || gv != v || gsw != sw {
				t.Errorf("stamp(command(%q, %+v)) = %q, %+v, %v", v, sw, gv, gsw, ok)
			}
		}
	}
	if got, want := command("v1", Switches{RemindOff: true, CheckOff: true}), "stele hook --installed=v1 --remind=off --check=off"; got != want {
		t.Errorf("command = %q, want %q", got, want)
	}
	if got, want := command("v1", Switches{}), "stele hook --installed=v1"; got != want {
		t.Errorf("command = %q, want %q", got, want)
	}
}

func TestStampRejects(t *testing.T) {
	for _, c := range []string{
		"stele hook --installed=v1 --remind=on",
		"stele hook --installed=v1 --check=off --remind=off",
		"stele hook --installed=v1 --remind=off --remind=off",
		"stele hook --installed=v1 --remind=off extra",
		"stele hook --installed=v1 --verbose",
		"stele hook --remind=off --installed=v1",
	} {
		if v, sw, ok := (handler{Command: c}).stamp(); ok {
			t.Errorf("stamp(%q) = %q, %+v, true; want not stamped", c, v, sw)
		}
	}
}

func TestInstallSwitches(t *testing.T) {
	plain, changed, _, err := Install(nil, "v1.2.0", Switches{})
	if err != nil || !changed {
		t.Fatalf("Install = changed %v, err %v", changed, err)
	}
	if out, changed, _, _ := Install(plain, "v1.2.0", Switches{}); changed || string(out) != string(plain) {
		t.Errorf("Install with the same switches changed it:\n%s", out)
	}
	off, changed, _, err := Install(plain, "v1.2.0", Switches{RemindOff: true})
	if err != nil || !changed {
		t.Fatalf("Install remind off = changed %v, err %v", changed, err)
	}
	if n := strings.Count(string(off), `"stele hook --installed=v1.2.0 --remind=off"`); n != 4 {
		t.Errorf("%d handlers carry --remind=off, want 4:\n%s", n, off)
	}
	if _, changed, _, _ := Install(off, "v1.2.0", Switches{RemindOff: true}); changed {
		t.Error("Install with the same switches again changed it")
	}
	back, changed, _, _ := Install(off, "v1.2.0", Switches{})
	if !changed || string(back) != string(plain) {
		t.Errorf("Install back to the defaults = changed %v:\n%s", changed, back)
	}
	// One handler out of step: rewritten.
	mixed := strings.Replace(string(off), "--installed=v1.2.0 --remind=off", "--installed=v1.2.0", 1)
	out, changed, _, _ := Install([]byte(mixed), "v1.2.0", Switches{RemindOff: true})
	if !changed || string(out) != string(off) {
		t.Errorf("Install of mixed handlers = changed %v:\n%s", changed, out)
	}
}

func TestInstalledSwitches(t *testing.T) {
	for _, sw := range allSwitches {
		out, _, _, err := Install(nil, "v1.2.0", sw)
		if err != nil {
			t.Fatal(err)
		}
		v, got, ok := Installed(out)
		if !ok || v != "v1.2.0" || got != sw {
			t.Errorf("Installed = %q, %+v, %v; want v1.2.0, %+v", v, got, ok, sw)
		}
	}
}

func TestUninstallSwitches(t *testing.T) {
	out, _, _, err := Install(nil, "v1.2.0", Switches{RemindOff: true, CheckOff: true})
	if err != nil {
		t.Fatal(err)
	}
	back, removed, empty, err := Uninstall(out)
	if err != nil || removed != 4 || !empty || string(back) != "{}\n" {
		t.Errorf("Uninstall = removed %d, empty %v, err %v:\n%s", removed, empty, err, back)
	}
}

func TestOtherSteleHookSwitches(t *testing.T) {
	off, _, _, err := Install(nil, "v1.2.0", Switches{RemindOff: true})
	if err != nil {
		t.Fatal(err)
	}
	if OtherSteleHook(off, "v1.2.0", Switches{RemindOff: true}) {
		t.Error("the same flagged handler counts as another")
	}
	if !OtherSteleHook(off, "v1.2.0", Switches{}) {
		t.Error("a handler with other switches does not count as another")
	}
}

func TestHookSwitches(t *testing.T) {
	tests := []struct {
		args []string
		want Switches
	}{
		{nil, Switches{}},
		{[]string{"--installed=v1"}, Switches{}},
		{[]string{"--installed=v1", "--remind=off"}, Switches{RemindOff: true}},
		{[]string{"--check=off", "--remind=off"}, Switches{RemindOff: true, CheckOff: true}},
		{[]string{"--remind=on"}, Switches{}},
		{[]string{"--remind=maybe", "--verbose", "x"}, Switches{}},
		{[]string{"--remind=off", "--remind=on"}, Switches{}},
	}
	for _, tt := range tests {
		if got := HookSwitches(tt.args); got != tt.want {
			t.Errorf("HookSwitches(%q) = %+v, want %+v", tt.args, got, tt.want)
		}
	}
}

func TestSwitchesString(t *testing.T) {
	want := []string{"remind on, check on", "remind off, check on", "remind on, check off", "remind off, check off"}
	for i, sw := range allSwitches {
		if got := sw.String(); got != want[i] {
			t.Errorf("%+v.String() = %q, want %q", sw, got, want[i])
		}
	}
}
