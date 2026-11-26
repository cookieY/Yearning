import { useState } from "react";
import { useTranslation } from "react-i18next";
import { UserRound } from "lucide-react";
import type { User, UserDeletionImpact } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  useCreateUser,
  useResetUserPassword,
  useUpdateUser,
} from "@/features/admin/use-admin";
import { describeError, describeErrorText } from "@/shared/api/error-display";
import { PendingButton } from "@/shared/components/pending-button";
import { appToast as toast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { LoadingState } from "@/shared/components/status/loading-state";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Button } from "@/shared/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/shared/components/ui/dialog";
import { Input } from "@/shared/components/ui/input";
import { Label } from "@/shared/components/ui/label";
import { PasswordInput } from "@/shared/components/ui/password-input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/shared/components/ui/select";
import i18next from "@/shared/i18n";

interface UserFormState {
  username: string;
  displayName: string;
  email: string;
  role: "user" | "admin";
  password: string;
  confirmPassword: string;
}

export function DeleteUserDialog({
  user,
  open,
  impact,
  loading,
  submitting,
  errorText,
  onCancel,
  onConfirm,
}: {
  user: User;
  open: boolean;
  impact: UserDeletionImpact | null;
  loading: boolean;
  submitting: boolean;
  errorText: string | null;
  onCancel: () => void;
  onConfirm: () => Promise<void>;
}) {
  const { t } = useTranslation();
  return (
    <Dialog open={open} onOpenChange={(next) => { if (!next) onCancel(); }}>
      <DialogContent data-testid="admin-user-delete-dialog">
        <DialogHeader>
          <DialogTitle>{t("adminUsers.delete.title", { name: user.username })}</DialogTitle>
          <DialogDescription>{t("adminUsers.delete.description")}</DialogDescription>
        </DialogHeader>
        {loading && <LoadingState variant="inline" />}
        {impact !== null && (
          <div className="flex flex-col gap-2 text-sm" data-testid="admin-user-delete-impact">
            <ImpactRow label={t("adminUsers.delete.flowReferences")} value={impact.flow_actor_references} />
            <ImpactRow label={t("adminUsers.delete.groupMemberships")} value={impact.permission_group_memberships} />
            <ImpactRow label={t("adminUsers.delete.activeGrants")} value={impact.active_query_grants} />
            <ImpactRow label={t("adminUsers.delete.activeSessions")} value={impact.active_query_sessions} />
            <p className="text-muted-foreground text-xs">{t("adminUsers.delete.snapshotsPreserved")}</p>
            {impact.blockers.length > 0 && (
              <Alert variant="destructive">
                <AlertTitle>{t("adminUsers.delete.blocked")}</AlertTitle>
                <AlertDescription>
                  <ul className="list-disc pl-4">
                    {impact.blockers.map((blocker) => {
                      // §18.54: blocker codes are a backend/mock string set;
                      // render the translated sentence, raw code only as an
                      // unknown-code fallback.
                      const key = `adminUsers.delete.blocker.${blocker.code}`;
                      const text = i18next.exists(key) ? i18next.t(key) : blocker.code;
                      return (
                        <li key={blocker.code}>
                          {text} × {blocker.count}
                        </li>
                      );
                    })}
                  </ul>
                </AlertDescription>
              </Alert>
            )}
          </div>
        )}
        {errorText !== null && (
          <Alert variant="destructive" data-testid="admin-user-delete-error">
            <AlertDescription>{errorText}</AlertDescription>
          </Alert>
        )}
        <DialogFooter>
          <Button variant="outline" onClick={onCancel}>
            {t("common.cancel")}
          </Button>
          <PendingButton
            variant="destructive"
            pending={submitting}
            disabled={loading || submitting || (impact !== null && !impact.can_delete)}
            onClick={() => void onConfirm()}
            data-testid="admin-user-delete-confirm"
          >
            {t("adminUsers.delete.confirm")}
          </PendingButton>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function ImpactRow({ label, value }: { label: string; value: number }) {
  return (
    <div className="flex flex-row items-center justify-between gap-4">
      <span className="text-muted-foreground text-xs">{label}</span>
      <span className="tabular-nums text-sm">{value}</span>
    </div>
  );
}

export function CreateUserDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { t } = useTranslation();
  const createUser = useCreateUser();
  const [form, setForm] = useState<UserFormState>({
    username: "",
    displayName: "",
    email: "",
    role: "user",
    password: "",
    confirmPassword: "",
  });
  const [errorText, setErrorText] = useState<string | null>(null);

  // §18.54: the initial password must be retyped before the form can submit.
  const confirmMismatch =
    form.confirmPassword !== "" && form.confirmPassword !== form.password;
  // Email is optional; when filled it must look like an address, and the
  // failure must be visible instead of a silently disabled submit (§18.54).
  const emailInvalid =
    form.email.trim() !== "" && !/.+@.+\..+/.test(form.email);

  const valid =
    form.username.trim() !== "" &&
    form.displayName.trim() !== "" &&
    form.password.length >= 12 &&
    form.confirmPassword !== "" &&
    !confirmMismatch &&
    !emailInvalid;

  const submit = async () => {
    setErrorText(null);
    try {
      await createUser.mutateAsync({
        username: form.username,
        display_name: form.displayName,
        email: form.email.trim() === "" ? null : form.email,
        role: form.role,
        password: form.password,
      });
      setForm({ username: "", displayName: "", email: "", role: "user", password: "", confirmPassword: "" });
      toast.success(t("common.createdToast"), { description: form.username });
      onClose();
    } catch (error) {
      const msg = describeErrorText(describeError(error, "createUser"));
      setErrorText(msg);
      toast.error(msg);
    }
  };

  return (
    <Dialog open={open} onOpenChange={(next) => { if (!next) onClose(); }}>
      <DialogContent data-testid="admin-user-create-dialog">
        <DialogHeader>
          <DialogTitle>{t("adminUsers.createTitle")}</DialogTitle>
          <DialogDescription>{t("adminUsers.createDescription")}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-3">
          <div className="flex flex-col gap-2">
            <Label htmlFor="user-create-username">{t("adminUsers.column.username")}</Label>
            <Input
              id="user-create-username"
              value={form.username}
              onChange={(event) => { setForm({ ...form, username: event.target.value }); }}
              maxLength={64}
              data-testid="user-create-username"
            />
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="user-create-display">{t("adminUsers.column.displayName")}</Label>
            <Input
              id="user-create-display"
              value={form.displayName}
              onChange={(event) => { setForm({ ...form, displayName: event.target.value }); }}
              maxLength={128}
              data-testid="user-create-display"
            />
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="user-create-email">{t("adminUsers.column.email")}</Label>
            <Input
              id="user-create-email"
              type="email"
              value={form.email}
              onChange={(event) => { setForm({ ...form, email: event.target.value }); }}
              aria-invalid={emailInvalid || undefined}
              data-testid="user-create-email"
            />
            {emailInvalid && (
              <p className="text-destructive text-xs" data-testid="user-create-email-invalid">
                {t("adminUsers.emailInvalid")}
              </p>
            )}
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="user-create-role">{t("adminUsers.roleLabel")}</Label>
            <Select
              value={form.role}
              onValueChange={(value) => {
                if (value === "user" || value === "admin") {
                  setForm({ ...form, role: value });
                }
              }}
            >
              <SelectTrigger id="user-create-role" data-testid="user-create-role">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="user">{t("adminUsers.roleUser")}</SelectItem>
                <SelectItem value="admin">{t("adminUsers.roleAdmin")}</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="user-create-password">{t("adminUsers.password")}</Label>
            <PasswordInput
              id="user-create-password"
              autoComplete="new-password"
              value={form.password}
              onChange={(event) => { setForm({ ...form, password: event.target.value }); }}
              aria-invalid={(form.password.length > 0 && form.password.length < 12) || undefined}
              data-testid="user-create-password"
            />
            {form.password.length > 0 && form.password.length < 12 ? (
              <p role="alert" className="text-destructive text-xs" data-testid="user-create-password-too-short">
                {t("adminUsers.passwordTooShort", { count: form.password.length })}
              </p>
            ) : (
              <p className="text-muted-foreground text-xs">{t("adminUsers.passwordHint")}</p>
            )}
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="user-create-confirm-password">{t("adminUsers.confirmPassword")}</Label>
            <PasswordInput
              id="user-create-confirm-password"
              autoComplete="new-password"
              value={form.confirmPassword}
              onChange={(event) => { setForm({ ...form, confirmPassword: event.target.value }); }}
              aria-invalid={confirmMismatch || undefined}
              data-testid="user-create-confirm-password"
            />
            {confirmMismatch && (
              <p className="text-destructive text-xs" data-testid="user-create-confirm-mismatch">
                {t("adminUsers.confirmMismatch")}
              </p>
            )}
          </div>
          {errorText !== null && (
            <Alert variant="destructive" data-testid="admin-user-create-error">
              <AlertDescription>{errorText}</AlertDescription>
            </Alert>
          )}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <PendingButton pending={createUser.isPending} disabled={!valid} onClick={() => void submit()} data-testid="user-create-submit">
            {t("adminUsers.create")}
          </PendingButton>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export function EditUserDialog({ user, onClose }: { user: User; onClose: () => void }) {
  const { t } = useTranslation();
  const updateUser = useUpdateUser();
  const resetPassword = useResetUserPassword();
  const [currentVersion, setCurrentVersion] = useState(user.version);
  const [displayName, setDisplayName] = useState(() => user.display_name ?? "");
  const [email, setEmail] = useState(() => user.email ?? "");
  const [role, setRole] = useState<"user" | "admin">(() =>
    user.is_builtin_admin || user.role === "admin" ? "admin" : "user",
  );
  const [errorText, setErrorText] = useState<string | null>(null);
  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [passwordError, setPasswordError] = useState<string | null>(null);

  const emailInvalid = email.trim() !== "" && !/.+@.+\..+/.test(email);
  const authProvider = user.auth_provider ?? "local";
  const passwordResetEditable = !user.is_builtin_admin && authProvider === "local";
  const passwordTooShort = newPassword.length > 0 && newPassword.length < 12;
  const passwordMismatch = confirmPassword !== "" && newPassword !== confirmPassword;
  const canResetPassword = newPassword.length >= 12 && confirmPassword === newPassword;

  const submit = async () => {
    setErrorText(null);
    try {
      await updateUser.mutateAsync({
        userId: user.id,
        version: currentVersion,
        body: {
          display_name: displayName,
          email: email.trim() === "" ? null : email,
          role: user.is_builtin_admin ? undefined : role,
        },
      });
      toast.success(t("common.savedToast"), { description: user.username });
      onClose();
    } catch (error) {
      const msg = describeErrorText(describeError(error, "updateUser"));
      setErrorText(msg);
      toast.error(msg);
    }
  };

  const submitPasswordReset = async () => {
    if (!passwordResetEditable || !canResetPassword) return;
    setPasswordError(null);
    try {
      await resetPassword.mutateAsync({
        userId: user.id,
        body: { new_password: newPassword },
      });
      setCurrentVersion((version) => version + 1);
      setNewPassword("");
      setConfirmPassword("");
      toast.success(t("adminUsers.resetPasswordSuccess"), { description: user.username });
    } catch (error) {
      const msg = describeErrorText(describeError(error, "resetUserPassword"), { detailSeparator: " " });
      setPasswordError(msg);
      toast.error(msg);
    }
  };

  return (
    <Dialog open onOpenChange={(next) => { if (!next) onClose(); }}>
      <DialogContent data-testid="admin-user-edit-dialog">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <UserRound className="size-4" />
            {t("adminUsers.editTitle", { name: user.username })}
          </DialogTitle>
          <DialogDescription>{t("adminUsers.editDescription")}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-3">
          <div className="flex flex-col gap-2">
            <Label htmlFor="user-edit-display">{t("adminUsers.column.displayName")}</Label>
            <Input
              id="user-edit-display"
              value={displayName}
              onChange={(event) => { setDisplayName(event.target.value); }}
              maxLength={128}
              data-testid="user-edit-display"
            />
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="user-edit-email">{t("adminUsers.column.email")}</Label>
            <Input
              id="user-edit-email"
              type="email"
              value={email}
              onChange={(event) => { setEmail(event.target.value); }}
              aria-invalid={emailInvalid || undefined}
              data-testid="user-edit-email"
            />
            {emailInvalid && (
              <p className="text-destructive text-xs" data-testid="user-edit-email-invalid">
                {t("adminUsers.emailInvalid")}
              </p>
            )}
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="user-edit-role">{t("adminUsers.roleLabel")}</Label>
            <Select
              value={role}
              disabled={user.is_builtin_admin}
              onValueChange={(value) => {
                if (value === "user" || value === "admin") {
                  setRole(value);
                }
              }}
            >
              <SelectTrigger id="user-edit-role" data-testid="user-edit-role">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="user">{t("adminUsers.roleUser")}</SelectItem>
                <SelectItem value="admin">{t("adminUsers.roleAdmin")}</SelectItem>
              </SelectContent>
            </Select>
            {user.is_builtin_admin && (
              <p className="text-muted-foreground text-xs">{t("adminUsers.builtinRoleLocked")}</p>
            )}
          </div>
          {errorText !== null && (
            <Alert variant="destructive" data-testid="admin-user-edit-error">
              <AlertDescription>{errorText}</AlertDescription>
            </Alert>
          )}
          <div className="flex flex-col gap-3 border-t pt-4" data-testid="user-edit-password-section">
            <div className="flex flex-col gap-1">
              <p className="text-sm font-medium">{t("adminUsers.resetPasswordTitle")}</p>
              {user.is_builtin_admin ? (
                <p className="text-muted-foreground text-xs" data-testid="user-edit-password-builtin-hint">
                  {t("adminUsers.resetPasswordBuiltinLocked")}
                </p>
              ) : authProvider !== "local" ? (
                <p className="text-muted-foreground text-xs" data-testid="user-edit-password-external-hint">
                  {t("adminUsers.resetPasswordExternalLocked")}
                </p>
              ) : (
                <p className="text-muted-foreground text-xs">{t("adminUsers.resetPasswordHint")}</p>
              )}
            </div>
            {passwordResetEditable && (
              <>
                <div className="flex flex-col gap-2">
                  <Label htmlFor="user-edit-new-password">{t("adminUsers.newPassword")}</Label>
                  <PasswordInput
                    id="user-edit-new-password"
                    value={newPassword}
                    onChange={(event) => { setNewPassword(event.target.value); }}
                    minLength={12}
                    autoComplete="new-password"
                    aria-invalid={passwordTooShort || undefined}
                    data-testid="user-edit-new-password"
                  />
                  {passwordTooShort ? (
                    <p role="alert" className="text-destructive text-xs" data-testid="user-edit-password-too-short">
                      {t("adminUsers.passwordTooShort", { count: newPassword.length })}
                    </p>
                  ) : (
                    <p className="text-muted-foreground text-xs">{t("adminUsers.passwordHint")}</p>
                  )}
                </div>
                <div className="flex flex-col gap-2">
                  <Label htmlFor="user-edit-confirm-password">{t("adminUsers.confirmNewPassword")}</Label>
                  <PasswordInput
                    id="user-edit-confirm-password"
                    value={confirmPassword}
                    onChange={(event) => { setConfirmPassword(event.target.value); }}
                    autoComplete="new-password"
                    aria-invalid={passwordMismatch || undefined}
                    data-testid="user-edit-confirm-password"
                  />
                  {passwordMismatch && (
                    <p className="text-destructive text-xs" data-testid="user-edit-password-mismatch">
                      {t("adminUsers.confirmMismatch")}
                    </p>
                  )}
                </div>
                {passwordError !== null && (
                  <Alert variant="destructive" data-testid="user-edit-password-error">
                    <AlertDescription>{passwordError}</AlertDescription>
                  </Alert>
                )}
              </>
            )}
          </div>
        </div>
        <DialogFooter data-testid="admin-user-edit-footer">
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          {passwordResetEditable && (
            <PendingButton
              type="button"
              variant="outline"
              pending={resetPassword.isPending}
              disabled={!canResetPassword}
              onClick={() => void submitPasswordReset()}
              data-testid="user-edit-reset-password-submit"
            >
              {t("adminUsers.resetPasswordSubmit")}
            </PendingButton>
          )}
          <PendingButton
            pending={updateUser.isPending}
            disabled={displayName.trim() === "" || emailInvalid}
            onClick={() => void submit()}
            data-testid="user-edit-submit"
          >
            {t("common.save")}
          </PendingButton>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
