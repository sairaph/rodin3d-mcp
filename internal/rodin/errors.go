package rodin

import (
	"errors"
	"fmt"
)

// ErrUnauthorized matches every authentication failure: an AuthError, or a
// TokenSource that has no token to give.
var ErrUnauthorized = errors.New("not authorized")

// AuthError is an HTTP 401 that one token refresh did not cure.
type AuthError struct {
	Procedure string
	Code      string // body error code, e.g. UNAUTHORIZED
	Message   string
}

func (e *AuthError) Error() string {
	s := e.Procedure + ": not authorized (HTTP 401)"
	if e.Code != "" {
		s += " " + e.Code
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}

func (e *AuthError) Is(target error) bool { return target == ErrUnauthorized }

// HTTPError is a reply with a status outside 2xx (other than 401).
type HTTPError struct {
	Procedure string
	Status    int
	Code      string
	Message   string
}

func (e *HTTPError) Error() string {
	s := fmt.Sprintf("%s: HTTP %d", e.Procedure, e.Status)
	if e.Code != "" {
		s += " " + e.Code
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}

// ServiceError is a 2xx reply whose body carries {"error": CODE}.
type ServiceError struct {
	Procedure string
	Code      string
	Message   string // the reply's own message field, usually empty
}

func (e *ServiceError) Error() string {
	s := e.Procedure + ": " + e.Code
	if text := e.Text(); text != "" {
		s += ": " + text
	}
	return s
}

// Text is the message for the code: the web app's own text when the code is
// in its table, else the reply's message.
func (e *ServiceError) Text() string {
	if m, ok := WebMessages[e.Code]; ok && m != "" {
		return m
	}
	return e.Message
}

// NetError is a request that got no reply: connection refused, DNS, timeout.
type NetError struct {
	Procedure string
	Err       error
}

func (e *NetError) Error() string { return e.Procedure + ": " + e.Err.Error() }
func (e *NetError) Unwrap() error { return e.Err }

// WebMessages is the web app's error table: body codes and the text the web
// shows for them (main.3620f51a.js @5335664, notes 14 section 9). An empty
// text means the web handles the code silently.
var WebMessages = map[string]string{
	"NO_SUCH_GROUP":                     "Group not found",
	"NO_SUCH_USER":                      "The username of email not found. Plase try again.",
	"NO_SUCH_TASK":                      "The specified task does not exist.",
	"USER_NOT_FOUND":                    "Please login to proceed.",
	"GROUP_NOT_FOUND":                   "The group was not found. Check the availability of your group. Contact us for more information.",
	"INVALID_REQUEST":                   "The request is invalid. Send feedback to us!",
	"PERMISSION_DENIED":                 "You do not have permission to perform this operation.",
	"INSUFFICIENT_BALANCE":              "Insufficient balance. Please top up your account.",
	"INSUFFICIENT_UNFROZEN_BALANCE":     "Your available balance is insufficient. Please top up or wait for frozen credits to be released before trying again.",
	"TASK_INCOMPLETE":                   "The task is incomplete. Please wait a while.",
	"SUBSCRIPTION_REQUIRED":             "A subscription is required to access this feature.",
	"USER_NOT_IN_GROUP":                 "The user is not a member of the specified group. Contact your group administrator to get more information.",
	"CODE_NOT_EXIST":                    "",
	"VALIDATION_FAILED":                 "",
	"USER_LIMIT_REACHED":                "",
	"CODE_INVALID":                      "The provided code does not exist or has been used.",
	"CODE_LIMIT_REACHED":                "Redemption code usage limit reached.",
	"GROUP_EXPIRED":                     "The group has expired. Plase contact your group administrator for more information.",
	"ALREADY_LOGGEDIN":                  "The user is already logged in.",
	"WRONG_PASSWORD":                    "The password entered is incorrect. Please try again.",
	"USER_FORBIDDEN":                    "The user is forbidden from performing this operation.",
	"EXTERNAL_AUTH_FAILED":              "External authentication failed.",
	"EXTERNAL_ALREADY_BINDED":           "The external account is already bound to another account. Please login.",
	"USER_ALREADY_REDEEMED_PUBLIC_CODE": "The user has already redeemed the public code.",
	"INVALID_EMAIL_VERIFICATION_CODE":   "The email verification code is invalid. Please try again.",
	"DUPLICATE_EMAIL":                   "The email address is already in use. Please login.",
	"WRONG_OLD_PASSWORD":                "The old password entered is not correct. Please try again.",
	"FAILED_TO_SEND":                    "Sorry, we can't send email to you. Please send feedback to us.",
	"RATE_LIMITED":                      "You've reached the rate limit. Please check your spam mailbox or try again later.",
	"VIDEO_EXISTS":                      "The video has been existed.",
	"TOO_MANY_IMAGES":                   "Please upload only one image at a time.",
	"UNSUPPORTED_IMAGE_TYPE":            "Unsupported image format. Please use JPG, PNG, or other common image formats.",
	"OBJECT_NOT_FOUND_ON_IMAGE":         "No recognizable object detected in the image. Please try with a clearer image.",
	"RISK_CONTROL_TRIGGERED":            "Risk control triggered. Please try again later or contact us for more information.",
	"INVALID_GITHUB_OAUTH_CALLBACK":     "OAuth login failed. Please try again later or contact us for more information.",
	"INVALID_GOOGLE_OAUTH_CALLBACK":     "OAuth login failed. Please try again later or contact us for more information.",
	"INVALID_GOOGLE_ACCESS_TOKEN":       "OAuth login failed. Please try again later or contact us for more information.",
	"INVALID_GITHUB_ACCESS_TOKEN":       "OAuth login failed. Please try again later or contact us for more information.",
	"NS_ERROR_FAILURE":                  "WebSocket connection failed. Please try again later or contact us for more information.",
	"INVALID_EMAIL":                     "The email is invalid. Please try again later.",
	"INSUFFICIENT_CHANCES":              "You have reached the maximum number of free private assets. To set this asset as private, please purchase a license.",
	"NO_HISTORY_AVAILABLE":              "Unable to fetch history. Please try again later.",
	"NO_IMAGE_UPLOADED":                 "We are unable to process your image at the moment (NO_IMAGE_UPLOADED). Please try again later.",
	"NO_MASK_CHANNEL_FOUND_ON_IMAGE":    "We are unable to process your image at the moment (NO_MASK_CHANNEL_FOUND_ON_IMAGE). Please try again later.",
	"ALREADY_GENERATED":                 "Your request has already been fulfilled. Please refresh the page.",
	"TASK_ALREADY_CONFIRMED":            "Your request has already been fulfilled. Please refresh the page.",
	"TASK_NOT_INITIALIZED":              "Task not initialized. Please try again later or contact us for more information.",
	"MODEL_NOT_CONFIRMED":               "Your model is not confirmed. Please try again later or contact us for more information.",
	"MAX_REDEMPTION_REACHED":            "Max redemption reached.",
	"NOT_YET_VERIFIED":                  "Unable to verify your request. Please try again later.",
	"SUBSCRIPTION_NOT_FOUND":            "Your subscription does not exist. Please contact us for more information.",
	"VIEW_WEIGHT_LENGTH_NOT_MATCH":      "View weight length not match. Please contact us for more information.",
	"CUSTOMER_NOT_FOUND":                "Customer not found. Please contact us for more information.",
	"INTERNAL_SERVER_ERROR":             "An unexpected error occurred. Please try again later or contact support.",
	"REFERENCE_IMAGE_NOT_EXIST":         "The reference image was not found. Please upload your image again.",
	"NO_REFERENCE_IMAGE_NAME":           "Please provide a name for your reference image.",
	"EXACTLY_ONE_MODEL_REQUIRED":        "Please select exactly one model for this operation.",
	"NO_SUCH_VERSION":                   "The specified model version does not exist.",
	"BAD_TASK_TYPE":                     "Invalid task type. Please check your operation and try again.",
	"INCORRECT_REFERENCE_DTYPE":         "The reference image format is not supported. Please use a different image.",
	"FILE_INDEX_OUT_OF_BOUNDS":          "The selected file index is invalid. Please try again.",
	"UNSUPPORTED_FILE_TYPE":             "This file type is not supported. Please use supported file formats.",
	"NO_MODEL_FILE_OR_IMAGE_FILE_FOUND": "No model or image file was found. Please upload required files.",
	"NO_REFERENCE_TASK_FOUND":           "The reference task was not found. Please start a new generation task.",
	"REFERRAL_UNKNOWN_ERROR":            "An unknown error occurred with the referral. Please try again later.",
	"REFERRAL_PAYPAL_ID_REQUIRED":       "PayPal ID is required for referral program. Please add your PayPal ID.",
	"REFEREE_BANNED":                    "This referred account has been banned from the referral program.",
	"REFERRER_BANNED":                   "Your account has been banned from the referral program.",
	"REFERRAL_RELATIONSHIP_EXISTS":      "A referral relationship already exists for this account.",
	"REFERRAL_REFER_SELF":               "You cannot refer yourself to the referral program.",
	"REFERRAL_CODE_NOT_FOUND":           "The referral code entered was not found. Please check and try again.",
	"OPERATION_NOT_SUPPORTED":           "The request cannot be fulfilled because the conditions do not match.",
	"NO_SUCH_A_PLAN":                    "",
	"IMAGE_CONTENT_VIOLATION":           "",
}
