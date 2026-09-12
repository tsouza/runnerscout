package admission

import "testing"

func TestRedeliveryAndBoundedRearming(t *testing.T) {
	s := State{}
	n, e := s.Reconcile(3, 0, 10, true)
	if e != nil || n != 3 {
		t.Fatal(n, e)
	}
	for range 100 {
		n, e = s.Reconcile(3, 0, 10, true)
		if e != nil || n != 0 {
			t.Fatal("expired/redelivered demand rearmed", n, e)
		}
	}
	_, _ = s.Reconcile(0, 1, 10, false)
	if s.Admitted != 3 {
		t.Fatal("reset with cleanup pending")
	}
	n, e = s.Reconcile(3, 0, 10, true)
	if e != nil || n != 3 || s.Cohort != 1 {
		t.Fatal(n, e, s)
	}
}
func TestAdmissionLimits(t *testing.T) {
	s := State{}
	n, e := s.Reconcile(100, 0, 10, true)
	if e != nil || n != 10 {
		t.Fatal(n, e)
	}
	if _, e = s.Reconcile(-1, 0, 10, true); e == nil {
		t.Fatal("accepted invalid demand")
	}
}

func TestLoweredLimitDrainsWithoutRearmingOrDroppingAdmissions(t *testing.T) {
	s := State{}
	if n, e := s.Reconcile(3, 0, 3, true); e != nil || n != 3 {
		t.Fatal(n, e)
	}
	if n, e := s.Reconcile(3, 3, 1, false); e != nil || n != 0 || s.Admitted != 3 {
		t.Fatal(n, e, s)
	}
	if n, e := s.Reconcile(3, 0, 1, true); e != nil || n != 0 || s.Admitted != 3 {
		t.Fatal("expired slots rearmed on limit change", n, e, s)
	}
	if n, e := s.Reconcile(0, 0, 1, true); e != nil || n != 0 || s.Admitted != 0 {
		t.Fatal(n, e, s)
	}
	if n, e := s.Reconcile(3, 0, 1, true); e != nil || n != 1 {
		t.Fatal(n, e)
	}
}
