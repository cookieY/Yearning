import { useState, type SyntheticEvent } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import { useMutation } from "@tanstack/react-query";
import { appToast as toast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { Languages, Palette, ShieldCheck, UserRound } from "lucide-react";
import { useSession } from "@/features/auth/session-provider";
import { changeMyPassword } from "@/api/generated/client/authentication/authentication";
import { SESSION_EXPIRED_EVENT } from "@/shared/api/mutator";
import { businessErrCodeByName, describeError } from "@/shared/api/error-display";
import { setLocale as persistLocale, type Locale } from "@/shared/i18n";
import { PageHeader, PageLayout } from "@/shared/components/page-layout";
import { PendingButton } from "@/shared/components/pending-button";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Badge } from "@/shared/components/ui/badge";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/shared/components/ui/card";
import { PasswordInput } from "@/shared/components/ui/password-input";
import { Label } from "@/shared/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/shared/components/ui/select";
import { Tabs, TabsList, TabsTrigger } from "@/shared/components/ui/tabs";
import { TransitionPanel } from "@/shared/components/ui/transition-panel";
import { ThemeToggle } from "@/app/shell/theme-toggle";

/**
 * Profile Center (UI spec §5.1: entered via top-right avatar, not in the sidebar; template Settings split layout).
 * Four sections: Profile (server facts, read-only — UpdateUserRequest is
 * an admin operation, no self-service surface), Account & Security (self-service
 * password change for local accounts, §18.52; LDAP/OIDC accounts keep the
 * identity-source note), Appearance (theme), Display (locale). The Notifications section was
 * removed per owner ruling (§18.69): channels are a server-side admin surface, so zero personal settings occupy no space.
 */

type ProfileTab = "identity" | "security" | "appearance" | "display";
const PROFILE_TABS: readonly ProfileTab[] = ["identity", "security", "appearance", "display"];

export default function ProfilePage() {
  const { t, i18n } = useTranslation();
  const { user } = useSession();
  const locale = i18n.resolvedLanguage ?? "zh-CN";
  const [tab, setTab] = useState<ProfileTab>("identity");

  return (
    <PageLayout data-testid="profile-page">
      <PageHeader
        icon={UserRound}
        title={t("profile.title")}
        description={t("profile.description")}
      />
      <div className="flex flex-col gap-5">
        <Tabs
          value={tab}
          onValueChange={(next) => {
            if (PROFILE_TABS.includes(next as ProfileTab)) {
              setTab(next as ProfileTab);
            }
          }}
        >
          <TabsList className="flex w-full justify-start overflow-x-auto overflow-y-hidden">
            <TabsTrigger value="identity" data-testid="profile-tab-identity">
              <UserRound className="size-3.5" />
              {t("profile.tabs.identity")}
            </TabsTrigger>
            <TabsTrigger value="security" data-testid="profile-tab-security">
              <ShieldCheck className="size-3.5" />
              {t("profile.tabs.security")}
            </TabsTrigger>
            <TabsTrigger value="appearance" data-testid="profile-tab-appearance">
              <Palette className="size-3.5" />
              {t("profile.tabs.appearance")}
            </TabsTrigger>
            <TabsTrigger value="display" data-testid="profile-tab-display">
              <Languages className="size-3.5" />
              {t("profile.tabs.display")}
            </TabsTrigger>
          </TabsList>
        </Tabs>

        <TransitionPanel activeIndex={PROFILE_TABS.indexOf(tab)}>
          <Card>
            <CardHeader>
              <CardTitle>{t("profile.identity.title")}</CardTitle>
              <CardDescription>{t("profile.identity.description")}</CardDescription>
            </CardHeader>
            <CardContent className="flex flex-col gap-3">
              <ProfileRow label={t("profile.identity.username")} value={user?.username ?? "—"} />
              <ProfileRow label={t("profile.identity.displayName")} value={user?.display_name ?? "—"} />
              <ProfileRow label={t("profile.identity.email")} value={user?.email ?? "—"} />
              <ProfileRow
                label={t("profile.identity.admin")}
                value={
                  user?.is_builtin_admin === true ? (
                    <Badge variant="secondary">{t("profile.identity.builtinAdmin")}</Badge>
                  ) : (
                    <Badge variant="outline">{t("profile.identity.regularUser")}</Badge>
                  )
                }
              />
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>{t("profile.security.title")}</CardTitle>
              <CardDescription>{t("profile.security.description")}</CardDescription>
            </CardHeader>
            <CardContent className="flex flex-col gap-3">
              {user?.auth_provider !== undefined && user.auth_provider !== "local" ? (
                <p className="text-muted-foreground text-xs" data-testid="profile-security-external-note">
                  {t("profile.security.identitySourceNote", {
                    provider: t(`profile.security.provider.${user.auth_provider}`),
                  })}
                </p>
              ) : (
                <PasswordChangeForm />
              )}
              {user?.is_builtin_admin === true && (
                <pre className="rounded-md border p-2 font-mono text-xs">
                  ./Yearning --reset-admin-password
                </pre>
              )}
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>{t("profile.appearance.title")}</CardTitle>
              <CardDescription>{t("profile.appearance.description")}</CardDescription>
            </CardHeader>
            <CardContent className="flex items-center justify-between gap-4">
              <span className="text-sm">{t("profile.appearance.theme")}</span>
              <ThemeToggle />
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>{t("profile.display.title")}</CardTitle>
              <CardDescription>{t("profile.display.description")}</CardDescription>
            </CardHeader>
            <CardContent className="flex flex-col gap-2">
              <Label htmlFor="profile-locale">{t("profile.display.locale")}</Label>
              <LocaleSelect
                locale={locale}
                onChange={(next) => {
                  void i18n.changeLanguage(next);
                  persistLocale(next as Locale);
                }}
              />
              <p className="text-muted-foreground text-xs">{t("profile.display.timezoneNote")}</p>
            </CardContent>
          </Card>
        </TransitionPanel>
      </div>
    </PageLayout>
  );
}

function ProfileRow({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="flex flex-row items-center justify-between gap-4 border-b pb-2 last:border-b-0">
      <span className="text-muted-foreground text-xs">{label}</span>
      <span className="text-sm">{value}</span>
    </div>
  );
}

function LocaleSelect({ locale, onChange }: { locale: string; onChange: (value: string) => void }) {
  const { t } = useTranslation();
  const [value, setValue] = useState(locale);
  return (
    <Select
      value={value}
      onValueChange={(next) => {
        if (next === null) return;
        setValue(next);
        onChange(next);
      }}
      items={{
        "zh-CN": t("profile.display.zhCN"),
        "en-US": t("profile.display.enUS"),
      }}
    >
      <SelectTrigger id="profile-locale" className="w-48" data-testid="profile-locale-select">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value="zh-CN" data-testid="profile-locale-zh">
          {t("profile.display.zhCN")}
        </SelectItem>
        <SelectItem value="en-US" data-testid="profile-locale-en">
          {t("profile.display.enUS")}
        </SelectItem>
      </SelectContent>
    </Select>
  );
}

const MIN_PASSWORD_LENGTH = 12;
const INVALID_CREDENTIALS_CODE = businessErrCodeByName("INVALID_CREDENTIALS");

/**
 * Self-service password change (§18.52, POST /users/me/password). The server
 * revokes every session on success — including this one — so the form
 * announces the session-expired event (the session provider flips the app to
 * anonymous and the route guard returns to /login) instead of keeping a
 * revoked session rendered.
 */
function PasswordChangeForm() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [oldPassword, setOldPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");

  const changeMutation = useMutation({
    mutationFn: () =>
      changeMyPassword({ old_password: oldPassword, new_password: newPassword }),
    onSuccess: () => {
      toast.success(t("profile.security.changed"));
      window.dispatchEvent(new CustomEvent(SESSION_EXPIRED_EVENT));
      void navigate("/login");
    },
    onError: (err) => {
      const display = describeError(err, "changeMyPassword");
      const isWrongOld =
        !display.contractViolation &&
        INVALID_CREDENTIALS_CODE !== null &&
        "err_code" in err &&
        (err as { err_code: unknown }).err_code === INVALID_CREDENTIALS_CODE;
      toast.error(t(isWrongOld ? "profile.security.wrongOldPassword" : display.messageKey));
    },
  });

  const confirmMismatch =
    confirmPassword !== "" && confirmPassword !== newPassword;
  const clientInvalid =
    oldPassword === "" ||
    newPassword.length < MIN_PASSWORD_LENGTH ||
    confirmPassword === "" ||
    confirmMismatch;

  const error = changeMutation.error;
  const errorDisplay = error == null ? null : describeError(error, "changeMyPassword");
  // 1101 in this operation means "old password wrong"; the shared catalog
  // copy is login-flavored, so this operation gives it its own sentence.
  const wrongOldPassword =
    !errorDisplay?.contractViolation &&
    INVALID_CREDENTIALS_CODE !== null &&
    error !== null &&
    typeof error === "object" &&
    "err_code" in error &&
    (error as { err_code: unknown }).err_code === INVALID_CREDENTIALS_CODE;
  const messageKey = errorDisplay === null
    ? null
    : wrongOldPassword
      ? "profile.security.wrongOldPassword"
      : errorDisplay.messageKey;

  const handleSubmit = (event: SyntheticEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (changeMutation.isPending || clientInvalid) return;
    changeMutation.mutate();
  };

  return (
    <form className="flex max-w-sm flex-col gap-3" onSubmit={handleSubmit} noValidate>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor="profile-old-password">{t("profile.security.oldPassword")}</Label>
        <PasswordInput
          id="profile-old-password"
          autoComplete="current-password"
          value={oldPassword}
          onChange={(event) => { setOldPassword(event.target.value); }}
          data-testid="profile-old-password"
        />
      </div>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor="profile-new-password">{t("profile.security.newPassword")}</Label>
        <PasswordInput
          id="profile-new-password"
          autoComplete="new-password"
          value={newPassword}
          onChange={(event) => { setNewPassword(event.target.value); }}
          aria-describedby="profile-new-password-hint"
          data-testid="profile-new-password"
        />
        <p id="profile-new-password-hint" className="text-muted-foreground text-xs">
          {t("profile.security.policyHint")}
        </p>
      </div>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor="profile-confirm-password">{t("profile.security.confirmPassword")}</Label>
        <PasswordInput
          id="profile-confirm-password"
          autoComplete="new-password"
          value={confirmPassword}
          onChange={(event) => { setConfirmPassword(event.target.value); }}
          aria-invalid={confirmMismatch || undefined}
          data-testid="profile-confirm-password"
        />
        {confirmMismatch && (
          <p className="text-destructive text-xs" data-testid="profile-confirm-mismatch">
            {t("profile.security.confirmMismatch")}
          </p>
        )}
      </div>
      {messageKey !== null && (
        <Alert variant="destructive" data-testid="profile-change-password-error">
          <AlertTitle>{t("profile.security.changeFailed")}</AlertTitle>
          <AlertDescription>{t(messageKey)}</AlertDescription>
        </Alert>
      )}
      <div>
        <PendingButton
          type="submit"
          pending={changeMutation.isPending}
          disabled={clientInvalid}
          data-testid="profile-change-password-submit"
        >
          {t("profile.security.submit")}
        </PendingButton>
      </div>
    </form>
  );
}
