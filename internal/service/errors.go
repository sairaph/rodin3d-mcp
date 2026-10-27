package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/sairaph/mcp-wizard/render"

	"github.com/sairaph/rodin3d-mcp/internal/auth"
	"github.com/sairaph/rodin3d-mcp/internal/config"
	"github.com/sairaph/rodin3d-mcp/internal/domain"
	"github.com/sairaph/rodin3d-mcp/internal/price"
	"github.com/sairaph/rodin3d-mcp/internal/rodin"
	"github.com/sairaph/rodin3d-mcp/internal/store"
)

// Domain error codes beyond the render set.
const (
	CodeInsufficientCredits = "insufficient_credits"
	CodeService             = "service_error"
)

// Hints shared by several errors.
const (
	hintLogin      = "Call login with no arguments, give the user the link it returns, and after they approve call get_account."
	hintNewLogin   = "Call login with no arguments to get a new sign-in link."
	hintPasteAgain = "The sign-in is still pending. Ask the user for the whole address of the page that cannot connect " +
		"(it starts with http://127.0.0.1:) and call login again with it as redirect_url. Start a new sign-in only if the link expired."
	hintOlderAddress = "The sign-in is still pending. Ask the user for the address shown after approving the latest link " +
		"and call login again with it as redirect_url; if they only have an older one, call login with no arguments for a new link."
	hintListTasks = "Call list_tasks to see the task ids (an id prefix of 8 or more characters works too)."
	hintRetry     = "Retry the call in a moment; if it keeps failing, run `" + domain.BinaryName + " doctor`."
	hintInternal  = "This is not caused by the arguments. Run `" + domain.BinaryName + " doctor`, then retry."
)

// Failure is an error that already carries its structured form.
type Failure struct{ E render.Error }

func (f *Failure) Error() string { return f.E.Message }

func fail(code, message, hint string) *Failure {
	return &Failure{render.Error{Code: code, Message: message, Hint: hint}}
}

func failFields(code, message, hint string, fields map[string]any) *Failure {
	return &Failure{render.Error{Code: code, Message: message, Hint: hint, Fields: fields}}
}

// serviceCodes maps the web's body error codes to error codes (plan 2.8).
var serviceCodes = map[string]string{
	"NO_SUCH_TASK": render.CodeNotFound, "NO_SUCH_VERSION": render.CodeNotFound, "REFERENCE_IMAGE_NOT_EXIST": render.CodeNotFound,
	"NO_REFERENCE_TASK_FOUND": render.CodeNotFound, "NO_SUCH_GROUP": render.CodeNotFound, "GROUP_NOT_FOUND": render.CodeNotFound,

	"NO_CURRENT_USER": render.CodeAuth, "UNAUTHORIZED": render.CodeAuth, "USER_NOT_FOUND": render.CodeAuth,

	"PERMISSION_DENIED": render.CodeForbidden, "SUBSCRIPTION_REQUIRED": render.CodeForbidden, "USER_FORBIDDEN": render.CodeForbidden,
	"SUBSCRIPTION_NOT_FOUND": render.CodeForbidden, "ACCOUNT_UNAVAILABLE": render.CodeForbidden, "INSUFFICIENT_CHANCES": render.CodeForbidden,

	"TASK_ALREADY_CONFIRMED": render.CodeConflict, "ALREADY_GENERATED": render.CodeConflict, "TASK_INCOMPLETE": render.CodeConflict,
	"TASK_NOT_INITIALIZED": render.CodeConflict, "MODEL_NOT_CONFIRMED": render.CodeConflict, "OPERATION_NOT_SUPPORTED": render.CodeConflict,
	"REVERSION_EXISTS": render.CodeConflict, "NO_HISTORY_AVAILABLE": render.CodeConflict,
	"RIGGING_NOT_FOUND": render.CodeConflict, "NO_SUCH_A_PLAN": render.CodeConflict,

	"INSUFFICIENT_BALANCE": CodeInsufficientCredits, "INSUFFICIENT_UNFROZEN_BALANCE": CodeInsufficientCredits,

	"INVALID_REQUEST": render.CodeInvalidInput, "TOO_MANY_IMAGES": render.CodeInvalidInput, "UNSUPPORTED_IMAGE_TYPE": render.CodeInvalidInput,
	"IMAGE_CONTENT_VIOLATION": render.CodeInvalidInput, "OBJECT_NOT_FOUND_ON_IMAGE": render.CodeInvalidInput,
	"VIEW_WEIGHT_LENGTH_NOT_MATCH": render.CodeInvalidInput, "IMAGE_LABEL_LENGTH_TOO_LONG": render.CodeInvalidInput,
	"UNSUPPORTED_FILE_TYPE": render.CodeInvalidInput, "INCORRECT_REFERENCE_DTYPE": render.CodeInvalidInput,
	"NO_IMAGE_UPLOADED": render.CodeInvalidInput, "NO_MASK_CHANNEL_FOUND_ON_IMAGE": render.CodeInvalidInput,
	"NO_REFERENCE_IMAGE_NAME": render.CodeInvalidInput, "EXACTLY_ONE_MODEL_REQUIRED": render.CodeInvalidInput,
	"BAD_TASK_TYPE": render.CodeInvalidInput, "FILE_INDEX_OUT_OF_BOUNDS": render.CodeInvalidInput,
	"NO_MODEL_FILE_OR_IMAGE_FILE_FOUND": render.CodeInvalidInput, "VALIDATION_FAILED": render.CodeInvalidInput,

	"RATE_LIMITED": render.CodeRateLimited, "RISK_CONTROL_TRIGGERED": render.CodeRateLimited,

	"INTERNAL_SERVER_ERROR": render.CodeUnavailable, "NS_ERROR_FAILURE": render.CodeUnavailable, "FILE_NOT_FOUND": render.CodeUnavailable,
}

// Classify turns any error of a call into the structured error the surfaces
// show. Every hint names the next call.
func Classify(err error) render.Error {
	if err == nil {
		return render.Error{}
	}
	var f *Failure
	if errors.As(err, &f) {
		return f.E
	}
	var lr *auth.LoginRequired
	if errors.As(err, &lr) {
		return render.Error{Code: render.CodeAuth, Message: capital(lr.Error()) + ".", Hint: hintLogin}
	}
	switch {
	case errors.Is(err, auth.ErrNotRedirect):
		// The approval the user gave is still good: never send them back to
		// a new link for a paste that was cut short.
		return render.Error{Code: render.CodeInvalidInput, Message: capital(err.Error()) + ".", Hint: hintPasteAgain}
	case errors.Is(err, auth.ErrStateMismatch):
		return render.Error{Code: render.CodeInvalidInput, Message: capital(err.Error()) + ".", Hint: hintOlderAddress}
	case errors.Is(err, auth.ErrNoPendingLogin), errors.Is(err, auth.ErrLoginExpired):
		return render.Error{Code: render.CodeInvalidInput, Message: capital(err.Error()) + ".", Hint: hintNewLogin}
	case errors.Is(err, store.ErrNotFound):
		return render.Error{Code: render.CodeNotFound, Message: "No task with that id.", Hint: hintListTasks}
	case errors.Is(err, context.Canceled):
		return render.Error{Code: render.CodeUnavailable, Message: "The call was cancelled before it finished.", Hint: "Send it again to retry."}
	case errors.Is(err, config.ErrMalformed):
		return render.Error{Code: render.CodeInvalidInput, Message: err.Error(), Hint: "Fix config.toml (every value is \"allow\" or \"deny\"; only \"deny\" denies) or delete it to allow every write."}
	}
	var rej *auth.LoginRejected
	if errors.As(err, &rej) {
		return render.Error{Code: render.CodeAuth, Message: capital(rej.Error()) + ".", Hint: hintNewLogin}
	}
	var oe *auth.OAuthError
	if errors.As(err, &oe) {
		if oe.Status >= 500 {
			return render.Error{Code: render.CodeUnavailable, Message: "The Hyper3D sign-in service failed: " + oe.Error(), Hint: hintRetry}
		}
		return render.Error{Code: render.CodeAuth, Message: "Hyper3D refused the sign-in: " + oe.Error(), Hint: hintNewLogin}
	}
	var amb *store.AmbiguousError
	if errors.As(err, &amb) {
		return render.Error{Code: render.CodeAmbiguous, Message: capital(amb.Error()) + ".", Hint: "Pass more characters of the task id, or the full id."}
	}
	var ae *rodin.AuthError
	if errors.As(err, &ae) || errors.Is(err, rodin.ErrUnauthorized) {
		return render.Error{Code: render.CodeAuth, Message: "Hyper3D did not accept the session (" + err.Error() + ").", Hint: hintLogin}
	}
	var se *rodin.ServiceError
	if errors.As(err, &se) {
		code, ok := serviceCodes[se.Code]
		if !ok {
			code = CodeService
		}
		msg := se.Code
		if text := se.Text(); text != "" {
			msg = text + " (" + se.Code + ")"
		}
		e := render.Error{Code: code, Message: "Hyper3D answered " + se.Procedure + ": " + msg, Fields: map[string]any{"service_code": se.Code}}
		switch code {
		case render.CodeAuth:
			e.Hint = hintLogin
		case render.CodeNotFound:
			e.Hint = hintListTasks
		case render.CodeConflict:
			e.Hint = "Call get_task to see the task's stage and the actions it allows."
		case CodeInsufficientCredits:
			e.Hint = "Call get_account to see the wallet; top up or wait for frozen credits to be released."
		case render.CodeRateLimited, render.CodeUnavailable:
			e.Hint = hintRetry
		default:
			e.Hint = "Check the arguments against get_generation_options, then retry."
		}
		return e
	}
	// A file host that refuses or fails is an expired signed address or a
	// passing outage, not a plan or id problem.
	var he *rodin.HTTPError
	var fne *rodin.NetError
	if (errors.As(err, &he) && strings.HasPrefix(he.Procedure, rodin.FetchPrefix)) ||
		(errors.As(err, &fne) && strings.HasPrefix(fne.Procedure, rodin.FetchPrefix)) {
		return render.Error{Code: render.CodeUnavailable, Message: "A file could not be fetched: " + err.Error(),
			Hint: "File addresses expire after a while: call the same tool again; it reads fresh addresses from Hyper3D."}
	}
	if errors.As(err, &he) {
		e := render.Error{Message: "Hyper3D answered " + he.Error(), Fields: map[string]any{"http_status": he.Status}}
		switch {
		case he.Status == http.StatusTooManyRequests:
			e.Code, e.Hint = render.CodeRateLimited, "Wait a minute, then retry."
		case he.Status == http.StatusForbidden:
			e.Code, e.Hint = render.CodeForbidden, "Call get_account to see the plan and privileges of the account."
		case he.Status == http.StatusNotFound:
			e.Code, e.Hint = render.CodeNotFound, hintListTasks
		case he.Status >= 500:
			e.Code, e.Hint = render.CodeUnavailable, hintRetry
		default:
			e.Code, e.Hint = render.CodeInvalidInput, "Check the arguments, then retry."
		}
		return e
	}
	var gate *price.Gate
	if errors.As(err, &gate) {
		return render.Error{Code: render.CodeForbidden, Message: "Not available on this plan: " + gate.Reason + ".", Hint: "Call get_account for the plan; choose an option the plan allows (get_generation_options lists the gates)."}
	}
	var pin *price.InvalidInput
	if errors.As(err, &pin) {
		return render.Error{Code: render.CodeInvalidInput, Message: capital(pin.Reason) + ".", Hint: "Call quote_cost with the values listed in its description."}
	}
	var ne *rodin.NetError
	var netErr net.Error
	if errors.As(err, &ne) || errors.As(err, &netErr) || errors.Is(err, context.DeadlineExceeded) {
		return render.Error{Code: render.CodeUnavailable, Message: "Hyper3D could not be reached: " + err.Error(), Hint: hintRetry}
	}
	return render.Error{Code: render.CodeInternal, Message: capital(err.Error()), Hint: hintInternal}
}

func capital(s string) string {
	if s == "" {
		return s
	}
	if s[0] >= 'a' && s[0] <= 'z' {
		return string(s[0]-'a'+'A') + s[1:]
	}
	return s
}

// wrapf adds what was being done to err, keeping its type for Classify.
func wrapf(err error, format string, args ...any) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf(format+": %w", append(args, err)...)
}
