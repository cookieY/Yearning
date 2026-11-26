import { useEffect, useState, type SyntheticEvent } from "react";
import "./login-glow.css";
import { useTranslation } from "react-i18next";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { SESSION_QUERY_KEY, useSession } from "@/features/auth/session-provider";
import {
  login,
  loginWithLdap,
  listAuthenticationProviders,
} from "@/api/generated/client/authentication/authentication";
import type { LoginRequest } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { describeError, isBusinessErrorCode } from "@/shared/api/error-display";
import { unwrapApi } from "@/shared/api/mutator";
import { setPublicBranding, usePublicBranding } from "@/shared/lib/branding";
import { ErrorState } from "@/shared/components/status/status-components";
import { LoadingState } from "@/shared/components/status/loading-state";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Button } from "@/shared/components/ui/button";
import { PendingButton } from "@/shared/components/pending-button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/shared/components/ui/card";
import { ShineBorder } from "@/shared/components/ui/shine-border";
import { BrandTypewriter } from "./brand-typewriter";
import { Input } from "@/shared/components/ui/input";
import { PasswordInput } from "@/shared/components/ui/password-input";
import { Label } from "@/shared/components/ui/label";
import { ShieldAlert } from "lucide-react";
import { BrandLogo } from "@/app/shell/brand-logo";
import { FOOTER_TEXT } from "@/app/shell/app-footer";

/**
 * Local / LDAP / OIDC login screen. Visible methods follow the server's
 * enabled-provider list (GET /auth/providers) — the page renders no method
 * the backend does not offer, including the credential form itself for
 * OIDC-only deployments. Credentials go only to the session endpoints; on
 * success the HttpOnly session cookie is set by the server and the session
 * query is invalidated so the route guard proceeds without any client-held
 * token. The unified error mapping keeps undeclared err_code outcomes on the
 * safe generic path; the admin lock-out special copy applies only to the
 * operation profile that actually declares it.
 *
 * The admin lock-out keeps the contract copy: after the fifth failed password
 * the page must show the server-side reset command (auth PRD §6) — there is
 * deliberately no web-based unlock path.
 */

type LoginMode = "local" | "ldap";

/** Only server-issued http(s) targets may become redirect targets (no javascript: URLs). */
function isSafeStartUrl(url: string): boolean {
  try {
    const parsed = new URL(url, window.location.origin);
    return parsed.protocol === "http:" || parsed.protocol === "https:";
  } catch {
    return false;
  }
}

export default function LoginPage() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const { retry: refreshSession } = useSession();
  const [mode, setMode] = useState<LoginMode>("local");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");

  const branding = usePublicBranding();
  const providersQuery = useQuery({
    queryKey: ["auth", "providers"],
    queryFn: () => unwrapApi(listAuthenticationProviders()),
    retry: false,
  });

  const providers = providersQuery.data;
  useEffect(() => {
    if (providers?.branding !== undefined) {
      setPublicBranding(providers.branding);
    }
  }, [providers]);
  const productName = (providers?.branding?.product_name ?? "").trim() || branding.productName;
  const loginDescription =
    providers?.branding !== undefined
      ? providers.branding.login_description.trim()
      : branding.loginDescription;
  const validOidcProviders = (providers?.oidc ?? []).filter((provider) =>
    isSafeStartUrl(provider.start_url),
  );
  const credentialFormVisible = providers !== undefined && (providers.local || providers.ldap);
  // An LDAP-only deployment (local=false, ldap=true) renders the credential
  // form without a mode switcher; it must talk to the LDAP endpoint even
  // though the default mode state says "local".
  const effectiveMode: LoginMode =
    providers !== undefined && !providers.local && providers.ldap ? "ldap" : mode;

  const loginMutation = useMutation({
    mutationFn: async (request: LoginRequest) => {
      if (effectiveMode === "ldap") {
        await loginWithLdap(request);
      } else {
        await login(request);
      }
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: SESSION_QUERY_KEY });
      refreshSession();
    },
  });

  const handleSubmit = (event: SyntheticEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (loginMutation.isPending) return;
    loginMutation.mutate({ username, password });
  };

  const switchMode = (next: LoginMode) => {
    setMode(next);
    // A previous failure belongs to the previous method; drop it.
    loginMutation.reset();
  };

  const error = loginMutation.error;
  const operationId = effectiveMode === "ldap" ? "loginWithLdap" : "login";
  const errorDisplay = error == null ? null : describeError(error, operationId);
  const isAdminLocked =
    !errorDisplay?.contractViolation && isBusinessErrorCode(error, "ADMIN_PASSWORD_LOCKED");

  return (
    // Owner ruling 2026-09-02: two-column auth layout per the reference
    // image — a brand panel with a color-shifting corner glow on the left
    // and the sign-in card on the right. Demo copy, fake logos and social
    // sign-in buttons from the reference stay out (design spec §3).
    <div className="grid min-h-svh overflow-hidden lg:grid-cols-2">
      <div className="relative hidden lg:flex lg:flex-col lg:justify-center lg:gap-6 p-12">
        <div className="auth-glow" aria-hidden />
        <div className="auth-glow-accent" aria-hidden />
        <div className="auth-glow-veil" aria-hidden />
        <div className="relative z-10 flex flex-col gap-6 lg:ml-[20%]">
          <div className="relative z-10 flex w-fit items-center gap-2 rounded-md border border-border/60 px-3 py-1.5 text-sm">
            <span className="bg-chart-series-user size-2 rounded-full" aria-hidden />
            {t("login.brandBadge", { productName })}
          </div>
          <h2 className="relative z-10 text-5xl leading-tight font-semibold tracking-tight whitespace-pre-line">
            {/* animated-text-03 typewriter over the WHOLE headline (owner
                ruling 2026-10-24). The invisible full phrase pins the final
                two-line geometry so the loop never changes the block's height
                (owner ruling: typewriter must not change the block's height); the typed overlay is
                absolutely positioned over it, and the sr-only twin keeps the
                full phrase in the accessible name. */}
            <span className="invisible" aria-hidden>
              {t("login.brandTitle")}
            </span>
            <span
              aria-hidden
              className="absolute inset-0 whitespace-pre-line"
            >
              <BrandTypewriter phrase={t("login.brandTitle")} />
            </span>
            <span className="sr-only">{t("login.brandTitle")}</span>
          </h2>
          <p className="relative z-10 max-w-md text-lg text-muted-foreground">
            {t("login.brandDescription", { productName })}
          </p>
        </div>
      </div>
      <div className="relative z-10 flex flex-col items-center justify-between p-6">
      <div className="flex w-full flex-1 items-center justify-center">
      {/* ShineBorder (owner ruling 2026-10-24): the gradient ring only exists
          under the cursor; at rest the card looks exactly as before. */}
      <ShineBorder
        borderWidth={2}
        gradient="from-shine-from via-shine-via to-shine-to"
        className="w-full max-w-sm"
      >
      <Card
        data-testid="login-card"
        className="relative h-full w-full rounded-2xl !border-0 bg-transparent !shadow-none !ring-0"
      >
        <CardHeader className="items-center text-center">
          <BrandLogo className="mx-auto mb-2" />
          <CardTitle className="text-xl tracking-tight">{t("login.title", { productName })}</CardTitle>
          <CardDescription
            className={loginDescription !== "" ? "whitespace-pre-line" : undefined}
            data-testid="login-description"
          >
            {loginDescription !== "" ? loginDescription : t("login.description", { productName })}
          </CardDescription>
        </CardHeader>
        <CardContent className="grid gap-4">
          {providersQuery.isPending && <LoadingState label={t("login.loadingProviders")} />}
          {providersQuery.isError && (
            <ErrorState
              error={providersQuery.error}
              operationId="listAuthenticationProviders"
              onRetry={() => {
                void providersQuery.refetch();
              }}
            />
          )}
          {providers !== undefined && validOidcProviders.length > 0 && (
            <div className="grid gap-2">
              {validOidcProviders.map((provider) => (
                <Button
                  key={provider.key}
                  type="button"
                  variant="outline"
                  className="h-9 w-full min-w-0 rounded-md shadow-none"
                  render={
                    <a href={provider.start_url} className="w-full truncate">
                      {t("login.oidcButton", { label: provider.label })}
                    </a>
                  }
                />
              ))}
            </div>
          )}
          {providers !== undefined && credentialFormVisible && validOidcProviders.length > 0 && (
            <div className="flex items-center gap-3 text-xs text-muted-foreground" role="separator">
              <span className="bg-border h-px flex-1" aria-hidden />
              {t("login.divider")}
              <span className="bg-border h-px flex-1" aria-hidden />
            </div>
          )}
          {credentialFormVisible && (
            <form onSubmit={handleSubmit} className="grid gap-4" noValidate>
              {providers.local && providers.ldap && (
                <div
                  className="border-border grid h-9 w-full grid-cols-2 border-b"
                  role="group"
                  aria-label={t("login.methodGroup")}
                >
                  {(
                    [
                      { key: "local", labelKey: "login.modeLocal" },
                      { key: "ldap", labelKey: "login.modeLdap" },
                    ] as const
                  ).map((item) => (
                    <button
                      key={item.key}
                      type="button"
                      aria-pressed={mode === item.key}
                      onClick={() => {
                        switchMode(item.key);
                      }}
                      className={
                        mode === item.key
                          ? "border-primary text-accent-foreground dark:text-foreground -mb-px inline-flex h-full items-center justify-center border-b-2 px-3 text-xs font-semibold transition-colors"
                          : "text-muted-foreground hover:text-foreground -mb-px inline-flex h-full items-center justify-center border-b-2 border-transparent px-3 text-xs font-medium transition-colors"
                      }
                    >
                      {t(item.labelKey)}
                    </button>
                  ))}
                </div>
              )}
              <div className="grid gap-2">
                <Label htmlFor="username">{t("login.username")}</Label>
                <Input
                  id="username"
                  name="username"
                  autoComplete="username"
                  className="h-9 rounded-md shadow-none"
                  value={username}
                  onChange={(event) => {
                    setUsername(event.target.value);
                  }}
                  placeholder={t("login.placeholder.username")}
                  required
                />
              </div>
              <div className="grid gap-2">
                <Label htmlFor="password">{t("login.password")}</Label>
                <PasswordInput
                  id="password"
                  groupClassName="h-9 rounded-md shadow-none"
                  name="password"
                  autoComplete="current-password"
                  value={password}
                  onChange={(event) => {
                    setPassword(event.target.value);
                  }}
                  placeholder={t("login.placeholder.password")}
                  required
                />
              </div>
              {error != null &&
                errorDisplay !== null &&
                (isAdminLocked ? (
                  <Alert variant="destructive">
                    <ShieldAlert />
                    <AlertTitle>{t("login.adminLocked.title")}</AlertTitle>
                    <AlertDescription>
                      {t("login.adminLocked.prompt")}{" "}
                      <code className="font-mono text-xs">./Yearning --reset-admin-password</code>
                    </AlertDescription>
                  </Alert>
                ) : (
                  <ErrorState error={error} operationId={operationId} />
                ))}
              <PendingButton
                type="submit"
                className="h-9 w-full rounded-md shadow-none"
                pending={loginMutation.isPending}
                disabled={loginMutation.isPending || username === "" || password === ""}
              >
                {t("login.submit")}
              </PendingButton>
            </form>
          )}
          {providers !== undefined && !credentialFormVisible && providers.oidc.length === 0 && (
            <p className="text-sm text-muted-foreground">{t("login.noMethods")}</p>
          )}
        </CardContent>
      </Card>
      </ShineBorder>
      </div>
      <footer className="mt-6 flex flex-wrap items-center justify-center gap-x-3 gap-y-1 text-center text-xs text-muted-foreground">
        <span>{FOOTER_TEXT}</span>
        <a
          href={__SOURCE_URL__}
          target="_blank"
          rel="noreferrer noopener"
          className="text-xs text-muted-foreground no-underline hover:no-underline hover:text-muted-foreground visited:text-muted-foreground focus-visible:text-muted-foreground transition-none"
        >
          {t("shell.footer.source")}
        </a>
      </footer>
      </div>
    </div>
  );
}
