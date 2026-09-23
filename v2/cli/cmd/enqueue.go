package cmd

import (
	"fmt"
	"time"

	"norn/v2/cli/api"
	"norn/v2/cli/style"
)

func finishEnqueue(response *api.EnqueueResponse) error {
	switch response.Status {
	case "succeeded":
		fmt.Println(style.SuccessBox.Render("complete"))
		return nil
	case "failed", "canceled":
		return fmt.Errorf("operation %s %s", response.OperationID, response.Status)
	default:
		// Enqueue retries can race operation completion. Re-read the durable
		// operation on every polling iteration; saga events are progress only.
		return streamViaDurablePolling(response.OperationID, response.SagaID, 2*time.Second)
	}
}
