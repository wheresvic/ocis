package middleware

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	revauser "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	"github.com/cs3org/reva/pkg/token/manager/jwt"
	acc "github.com/owncloud/ocis-accounts/pkg/proto/v0"
	"github.com/owncloud/ocis-pkg/v2/log"
	ocisoidc "github.com/owncloud/ocis-pkg/v2/oidc"
	"github.com/owncloud/ocis-proxy/pkg/config"
)

// AccountMiddlewareOption defines a single option function.
type AccountMiddlewareOption func(o *AccountMiddlewareOptions)

// AccountMiddlewareOptions defines the available options for this package.
type AccountMiddlewareOptions struct {
	// Logger to use for logging, must be set
	Logger log.Logger
	// TokenManagerConfig for communicating with the reva token manager
	TokenManagerConfig config.TokenManager
	// AccountsClient for resolving accounts
	AccountsClient acc.AccountsService
}

// Logger provides a function to set the logger option.
func Logger(l log.Logger) AccountMiddlewareOption {
	return func(o *AccountMiddlewareOptions) {
		o.Logger = l
	}
}

// TokenManagerConfig provides a function to set the token manger config option.
func TokenManagerConfig(cfg config.TokenManager) AccountMiddlewareOption {
	return func(o *AccountMiddlewareOptions) {
		o.TokenManagerConfig = cfg
	}
}

// AccountsClient provides a function to set the accounts client config option.
func AccountsClient(ac acc.AccountsService) AccountMiddlewareOption {
	return func(o *AccountMiddlewareOptions) {
		o.AccountsClient = ac
	}
}

func newAccountUUIDOptions(opts ...AccountMiddlewareOption) AccountMiddlewareOptions {
	opt := AccountMiddlewareOptions{}
	for _, o := range opts {
		o(&opt)
	}
	return opt
}

func getAccount(l log.Logger, claims ocisoidc.StandardClaims, ac acc.AccountsService) (account *acc.Account, status int) {
	entry, err := svcCache.Get(AccountsKey, claims.Email)
	if err != nil {
		l.Debug().Msgf("No cache entry for %v", claims.Email)
		resp, err := ac.ListAccounts(context.Background(), &acc.ListAccountsRequest{
			Query:    fmt.Sprintf("mail eq '%s'", strings.ReplaceAll(claims.Email, "'", "''")),
			PageSize: 2,
		})

		if err != nil {
			l.Error().Err(err).Str("email", claims.Email).Msgf("Error fetching from accounts-service")
			status = http.StatusInternalServerError
			return
		}

		if len(resp.Accounts) <= 0 {
			l.Error().Str("email", claims.Email).Msgf("Account not found")
			status = http.StatusNotFound
			return
		}

		// TODO provision account

		if len(resp.Accounts) > 1 {
			l.Error().Str("email", claims.Email).Msgf("More than one account with this email found. Not logging user in.")
			status = http.StatusForbidden
			return
		}

		err = svcCache.Set(AccountsKey, claims.Email, *resp.Accounts[0])
		if err != nil {
			l.Err(err).Str("email", claims.Email).Msgf("Could not cache user")
			status = http.StatusInternalServerError
			return
		}

		account = resp.Accounts[0]
	} else {
		a, ok := entry.V.(acc.Account) // TODO how can we directly point to the cached account?
		if !ok {
			status = http.StatusInternalServerError
			return
		}
		account = &a
	}
	return
}

// AccountUUID provides a middleware which mints a jwt and adds it to the proxied request based
// on the oidc-claims
func AccountUUID(opts ...AccountMiddlewareOption) func(next http.Handler) http.Handler {
	opt := newAccountUUIDOptions(opts...)

	return func(next http.Handler) http.Handler {
		// TODO: handle error
		tokenManager, err := jwt.New(map[string]interface{}{
			"secret":  opt.TokenManagerConfig.JWTSecret,
			"expires": int64(60),
		})
		if err != nil {
			opt.Logger.Fatal().Err(err).Msgf("Could not initialize token-manager")
		}

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			l := opt.Logger
			claims, ok := r.Context().Value(ClaimsKey).(ocisoidc.StandardClaims)
			if !ok {
				next.ServeHTTP(w, r)
				return
			}

			// TODO allow lookup by username?
			// TODO allow lookup by custom claim, eg an id

			account, status := getAccount(l, claims, opt.AccountsClient)
			if status != 0 {
				w.WriteHeader(status)
				return
			}

			l.Debug().Interface("claims", claims).Interface("account", account).Msgf("Associated claims with uuid")
			token, err := tokenManager.MintToken(r.Context(), &revauser.User{
				Id: &revauser.UserId{
					OpaqueId: account.Id,
				},
				Username:     account.PreferredName,
				DisplayName:  account.DisplayName,
				Mail:         account.Mail,
				MailVerified: account.ExternalUserState == "" || account.ExternalUserState == "Accepted",
				// TODO groups
			})

			if err != nil {
				l.Error().Err(err).Msgf("Could not mint token")
				w.WriteHeader(http.StatusInternalServerError)
				return
			}

			r.Header.Set("x-access-token", token)
			next.ServeHTTP(w, r)
		})
	}
}
