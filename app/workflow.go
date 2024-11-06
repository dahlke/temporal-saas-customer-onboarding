package app

import (
	"fmt"
	"temporal-saas-customer-onboarding/messages"
	"temporal-saas-customer-onboarding/types"
	"time"

	"go.temporal.io/sdk/workflow"
)

const (
	ACCEPTANCE_TIME = 120
)

// Add this type at the top of the file
type compensation func(workflow.Context) error

func OnboardingWorkflow(ctx workflow.Context, input types.OnboardingWorkflowInput) (string, error) {
	logger := workflow.GetLogger(ctx)

	options := workflow.ActivityOptions{
		StartToCloseTimeout: time.Second * 5,
	}

	ctx = workflow.WithActivityOptions(ctx, options)

	state := types.OnboardingWorkflowState{
		AccountName: input.AccountName,
		Emails:      input.Emails,
		ClaimCodes:  make([]types.ClaimCodeStatus, len(input.Emails)),
	}

	// Initialize claim codes for each email
	claimCodes := []string{"XXX", "YYY"}
	for i, email := range input.Emails {
		state.ClaimCodes[i] = types.ClaimCodeStatus{
			Email:     email,
			Code:      claimCodes[i],
			IsClaimed: false,
		}
	}

	err := messages.SetQueryHandlerForState(ctx, &state)
	if err != nil {
		return "", err
	}

	// TODO: throw and catch errors
	// TODO: custom search attributes
	// TODO: re-send claim codes signal
	// TODO: re-send welcome email signal
	// TODO: comments
	// TODO: set IsClaimed based on which claim code is accepted

	// Create compensation stack
	var compensations []compensation

	// Charge customer
	var chargeResult string
	err = workflow.ExecuteActivity(ctx, ChargeCustomer, input.AccountName).Get(ctx, &chargeResult)
	if err != nil {
		return "", err
	}
	logger.Info("Successfully charged customer", "result", chargeResult)
	// Add compensation
	compensations = append(compensations, func(ctx workflow.Context) error {
		var refundResult string
		return workflow.ExecuteActivity(ctx, RefundCustomer, input.AccountName).Get(ctx, &refundResult)
	})

	// Create account
	var createAccountResult string
	err = workflow.ExecuteActivity(ctx, CreateAccount, input.AccountName).Get(ctx, &createAccountResult)
	if err != nil {
		executeCompensations(ctx, compensations)
		return "", err
	}
	logger.Info("Successfully created account", "result", createAccountResult)
	compensations = append(compensations, func(ctx workflow.Context) error {
		var deleteResult string
		return workflow.ExecuteActivity(ctx, DeleteAccount, input.AccountName).Get(ctx, &deleteResult)
	})

	// Create admin users
	var createAdminUsersResult string
	err = workflow.ExecuteActivity(ctx, CreateAdminUsers, input.Emails).Get(ctx, &createAdminUsersResult)
	if err != nil {
		executeCompensations(ctx, compensations)
		return "", err
	}
	logger.Info("Successfully created admin users", "result", createAdminUsersResult)
	compensations = append(compensations, func(ctx workflow.Context) error {
		var deleteUsersResult string
		return workflow.ExecuteActivity(ctx, DeleteAdminUsers, input.Emails).Get(ctx, &deleteUsersResult)
	})

	// Make the claim code a hash of the emails?
	for _, claimCode := range state.ClaimCodes {
		var sendClaimCodeResult string
		err = workflow.ExecuteActivity(ctx, SendClaimCodes, input.AccountName, claimCode.Code).Get(ctx, &sendClaimCodeResult)
		if err != nil {
			logger.Error("Failed to send claim code", "error", err, "email", claimCode.Email)
			return "", err
		}
		logger.Info("Successfully sent claim code", "result", sendClaimCodeResult, "email", claimCode.Email)
	}

	if err != nil {
		return "", err
	}

	// Create a pointer to track the claimed status
	var claimed bool
	claimed, err = messages.SetUpdateHandlerForAcceptClaimCode(ctx, &claimed)
	if err != nil {
		return "", err
	}

	// Wait for up to ACCEPTANCE_TIME seconds for the update
	ok, _ := workflow.AwaitWithTimeout(ctx, time.Second*ACCEPTANCE_TIME, func() bool {
		return claimed
	})

	// If the update wasn't received or was false, fail the workflow
	if !ok {
		return "", fmt.Errorf("claim codes not accepted within %d seconds", ACCEPTANCE_TIME)
	}

	var sendWelcomeEmailResult string
	err = workflow.ExecuteActivity(ctx, SendWelcomeEmail, input.Emails).Get(ctx, &sendWelcomeEmailResult)
	if err != nil {
		logger.Error("Failed to send welcome email", "error", err)
		return "", err
	}
	logger.Info("Successfully sent welcome email", "result", sendWelcomeEmailResult)

	var sendFeedbackEmailResult string
	err = workflow.ExecuteActivity(ctx, SendFeedbackEmail, input.Emails).Get(ctx, &sendFeedbackEmailResult)
	if err != nil {
		logger.Error("Failed to send feedback email", "error", err)
		return "", err
	}
	logger.Info("Successfully sent feedback email", "result", sendFeedbackEmailResult)

	return sendFeedbackEmailResult, nil
}

func executeCompensations(ctx workflow.Context, compensations []compensation) {
	// TODO: review the failure modes and retry logic
	logger := workflow.GetLogger(ctx)
	// Execute compensations in reverse order
	for i := len(compensations) - 1; i >= 0; i-- {
		if err := compensations[i](ctx); err != nil {
			logger.Error("Compensation failed", "error", err)
			// Continue with other compensations even if one fails
		}
	}
}
