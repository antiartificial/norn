package store

import "testing"

func TestOperationPoolConfigRejectsDeadlockingPool(t *testing.T) {
	if _, err := operationPoolConfig("postgres://norn:norn@localhost/norn?pool_max_conns=1"); err == nil {
		t.Fatal("expected undersized pool to be rejected")
	}
	config, err := operationPoolConfig("postgres://norn:norn@localhost/norn?pool_max_conns=4")
	if err != nil {
		t.Fatal(err)
	}
	if config.MaxConns != 4 {
		t.Fatalf("max connections = %d", config.MaxConns)
	}
}
