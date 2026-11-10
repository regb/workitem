package daemonapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/regb/workitem/internal/coordinator"
)

func messageCommandID(itemID, messageID string) string {
	sum := sha256.Sum256([]byte(itemID + "\x00" + messageID))
	return "api-message:" + hex.EncodeToString(sum[:])
}

func deliveryResult(value coordinator.CommandDelivery) (bool, error) {
	switch value.State {
	case "confirmed":
		return true, nil
	case "not_sent", "cancelled":
		return false, precondition(fmt.Sprintf("message was not sent (%s); fix the reported condition, then explicitly submit the original task with a new client_message_id", value.Reason))
	case "uncertain":
		return false, precondition(fmt.Sprintf("message delivery is uncertain (%s); inspect native history before any resend", value.Reason))
	}
	return false, nil
}

func (s *Server) messageReceiptResult(receipt coordinator.APIMessageReceipt) (bool, error) {
	value, err := s.Core.Database().CommandDelivery(receipt.ItemID, receipt.RuntimeID, messageCommandID(receipt.ItemID, receipt.MessageID))
	if err != nil {
		return false, err
	}
	if receipt.Delivered && value.State == "" {
		value, err = s.Core.Database().LegacyCommandDelivery(receipt.ItemID, receipt.RuntimeID, messageCommandID(receipt.ItemID, receipt.MessageID))
		if err != nil {
			return false, err
		}
	}
	done, err := deliveryResult(value)
	if done || err != nil {
		return done, err
	}
	// Old receipts may have marked a queued handoff delivered. A semantic
	// observation takes precedence over that historical transport acknowledgement.
	return receipt.Delivered && value.State == "", nil
}

func (s *Server) waitMessageConfirmation(ctx context.Context, itemID, runtimeID, commandID string) error {
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		value, err := s.Core.Database().CommandDelivery(itemID, runtimeID, commandID)
		if err != nil {
			return err
		}
		done, err := deliveryResult(value)
		if done || err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return precondition("message accepted but native submission is not confirmed; do not resend with a new identity, inspect delivery events or retry the same client_message_id")
		case <-ticker.C:
		}
	}
}
