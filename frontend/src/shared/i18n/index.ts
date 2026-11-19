import i18next from "i18next";
import { initReactI18next } from "react-i18next";
import zhCN from "./locales/zh-CN.json";
import enUS from "./locales/en-US.json";

// i18n contract (yearning-ui-design-spec.md §11, migration contract §1):
// zh-CN is the default locale, the user's explicit choice outranks the browser
// language, and an unmatched browser language falls back to zh-CN. SQL, DDL,
// database product names and identifiers stay untranslated.
export const LOCALES = ["zh-CN", "en-US"] as const;
export type Locale = (typeof LOCALES)[number];
export const DEFAULT_LOCALE: Locale = "zh-CN";
const STORAGE_KEY = "yearning-locale";

export function resolveLocale(): Locale {
  const stored = localStorage.getItem(STORAGE_KEY);
  if (stored && (LOCALES as readonly string[]).includes(stored)) return stored as Locale;
  const browser = navigator.language;
  if (browser && (LOCALES as readonly string[]).includes(browser)) return browser as Locale;
  const prefix = browser.split("-")[0];
  if (prefix === "zh") return "zh-CN";
  if (prefix === "en") return "en-US";
  return DEFAULT_LOCALE;
}

export function setLocale(locale: Locale): void {
  localStorage.setItem(STORAGE_KEY, locale);
  void i18next.changeLanguage(locale);
  document.documentElement.lang = locale;
}

void i18next.use(initReactI18next).init({
  resources: {
    "zh-CN": { translation: zhCN },
    "en-US": { translation: enUS },
  },
  lng: resolveLocale(),
  fallbackLng: DEFAULT_LOCALE,
  interpolation: { escapeValue: false },
  returnNull: false,
});

document.documentElement.lang = i18next.language;
export default i18next;
