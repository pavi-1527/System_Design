package syncvv

import "testing"

func TestIncrementAndMergeDoNotMutateInputs(t *testing.T) {
	a := Vector{"laptop": 1}
	b := a.Increment("laptop").Increment("phone")
	if a["laptop"] != 1 || len(a) != 1 {
		t.Fatalf("Increment mutated its receiver: %v", a)
	}
	if b["laptop"] != 2 || b["phone"] != 1 {
		t.Fatalf("Increment chain = %v, want laptop 2, phone 1", b)
	}
	m := Merge(Vector{"laptop": 3, "phone": 1}, Vector{"phone": 4, "tablet": 1})
	if m["laptop"] != 3 || m["phone"] != 4 || m["tablet"] != 1 {
		t.Fatalf("Merge = %v, want entry-wise maximum", m)
	}
	var zero Vector
	if z := zero.Increment("x"); z["x"] != 1 {
		t.Fatalf("Increment on a nil vector = %v", z)
	}
}

func TestCompare_AllFourOrderings(t *testing.T) {
	cases := []struct {
		name string
		a, b Vector
		want Ordering
	}{
		{"equal", Vector{"l": 2, "p": 1}, Vector{"l": 2, "p": 1}, Equal},
		{"equal with explicit zero", Vector{"l": 2, "p": 0}, Vector{"l": 2}, Equal},
		{"before", Vector{"l": 1}, Vector{"l": 2}, Before},
		{"before with new device", Vector{"l": 1}, Vector{"l": 1, "p": 1}, Before},
		{"after", Vector{"l": 2, "p": 1}, Vector{"l": 2}, After},
		{"concurrent", Vector{"l": 2, "p": 1}, Vector{"l": 1, "p": 2}, Concurrent},
		{"concurrent disjoint", Vector{"l": 1}, Vector{"p": 1}, Concurrent},
		{"both empty", Vector{}, nil, Equal},
	}
	for _, c := range cases {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("%s: Compare(%v, %v) = %v, want %v", c.name, c.a, c.b, got, c.want)
		}
	}
}

func TestResolve_TakeKeepOrConflict(t *testing.T) {
	base := Vector{"laptop": 3}
	local := Version{Name: "budget.xlsx", Vector: base, Content: []byte("v3")}
	// The server has a newer version that descends from ours: take it.
	remote := Version{Name: "budget.xlsx", Vector: base.Increment("phone"), Content: []byte("v4")}
	if d := Resolve(local, remote); d.Action != TakeRemote {
		t.Fatalf("remote descends from local: %v, want take-remote", d.Action)
	}
	// We edited offline; the server still has what we last synced: keep ours (push).
	if d := Resolve(Version{Vector: base.Increment("laptop")}, Version{Vector: base}); d.Action != KeepLocal {
		t.Fatalf("local descends from remote: %v, want keep-local", d.Action)
	}
	if d := Resolve(local, local); d.Action != KeepLocal {
		t.Fatalf("identical versions: %v, want keep-local", d.Action)
	}
	// Both edited since the last sync: conflict, keep both.
	ours := Version{Name: "budget.xlsx", Vector: base.Increment("laptop"), Content: []byte("ours")}
	theirs := Version{Name: "budget.xlsx", Vector: base.Increment("phone"), Content: []byte("theirs")}
	d := Resolve(ours, theirs)
	if d.Action != Conflict {
		t.Fatalf("concurrent edits: %v, want conflict", d.Action)
	}
	if d.ConflictCopy != "budget.xlsx (conflicted copy from phone)" {
		t.Fatalf("ConflictCopy = %q", d.ConflictCopy)
	}
	if string(d.Local.Content) != "ours" || string(d.Remote.Content) != "theirs" {
		t.Fatal("a Conflict must carry both versions so neither edit is lost")
	}
	// Blame: the remote device with the most new edits; ties go to the smallest id.
	tie := Resolve(Version{Name: "n", Vector: Vector{"a": 1}}, Version{Name: "n", Vector: Vector{"b": 2, "c": 2}})
	if tie.ConflictCopy != "n (conflicted copy from b)" {
		t.Fatalf("tie-break = %q, want b", tie.ConflictCopy)
	}
}
