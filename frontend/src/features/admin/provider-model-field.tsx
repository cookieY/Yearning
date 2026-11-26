import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { listAiProviderModels } from "@/api/generated/client/administration/administration";
import { unwrapApi } from "@/shared/api/mutator";
import { Input } from "@/shared/components/ui/input";
import { Label } from "@/shared/components/ui/label";
import { ScrollArea } from "@/shared/components/ui/scroll-area";
import { usePortalAnchor } from "@/shared/lib/use-portal-anchor";

export interface ProviderModelFieldProps {
  modelName: string;
  baseUrl: string;
  apiKey: string;
  editingProviderId: string | null;
  onModelChange: (nextModel: string) => void;
}

/**
 * AI provider model input with debounced catalog autocomplete and a portaled
 * dropdown anchored outside the modal ScrollArea so options are never clipped.
 */
export function ProviderModelField({
  modelName,
  baseUrl,
  apiKey,
  editingProviderId,
  onModelChange,
}: ProviderModelFieldProps) {
  const { t } = useTranslation();
  // §18.76-B: model catalog autocomplete. Automatically fetches once base_url + api_key are present (or reusing stored
  // key in edit mode); 400ms debounce + sequence counter prevent stale writebacks. The catalog is only a suggestion list
  // and the input always allows manual entry; fetch failures silently degrade to manual entry (the probe still surfaces
  // the real error on save). Results are cached by key and loading is derived from key mismatch — no synchronous
  // setState inside the effect (react-hooks/set-state-in-effect).
  const catalogKey = `${baseUrl.trim()}\u0000${apiKey.trim()}\u0000${editingProviderId ?? ""}`;
  const canFetchCatalog =
    baseUrl.trim() !== "" && (apiKey.trim() !== "" || editingProviderId !== null);
  const [catalog, setCatalog] = useState<{
    key: string;
    state: "done" | "failed";
    items: string[];
  } | null>(null);
  const catalogSeq = useRef(0);
  const [suggestOpen, setSuggestOpen] = useState(false);
  const modelAnchorRef = useRef<HTMLDivElement>(null);
  const modelPopupRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!canFetchCatalog) return;
    const seq = ++catalogSeq.current;
    const timer = setTimeout(() => {
      void unwrapApi(
        listAiProviderModels({
          base_url: baseUrl.trim(),
          ...(apiKey.trim() !== "" ? { api_key: apiKey.trim() } : {}),
          ...(apiKey.trim() === "" && editingProviderId !== null
            ? { provider_id: editingProviderId }
            : {}),
        }),
      )
        .then((res) => {
          if (catalogSeq.current !== seq) return;
          setCatalog({ key: catalogKey, state: "done", items: res.items });
        })
        .catch(() => {
          if (catalogSeq.current !== seq) return;
          setCatalog({ key: catalogKey, state: "failed", items: [] });
        });
    }, 400);
    return () => {
      clearTimeout(timer);
      catalogSeq.current += 1;
    };
  }, [apiKey, baseUrl, canFetchCatalog, catalogKey, editingProviderId]);

  const catalogResult = catalog !== null && catalog.key === catalogKey ? catalog : null;
  const catalogView = !canFetchCatalog
    ? "idle"
    : catalogResult === null
      ? "loading"
      : catalogResult.state;
  const catalogItems = catalogView === "done" ? catalogResult?.items ?? [] : [];
  usePortalAnchor(suggestOpen && catalogItems.length > 0, modelAnchorRef, modelPopupRef, {
    preferredHeight: 200,
  });

  return (
    <div className="flex flex-col gap-1">
      <Label htmlFor="provider-model">{t("admin.providers.model")}</Label>
      <div ref={modelAnchorRef} className="relative">
        <Input
          id="provider-model"
          value={modelName}
          onChange={(event) => {
            onModelChange(event.target.value);
          }}
          onFocus={() => {
            setSuggestOpen(true);
          }}
          onBlur={() => {
            setSuggestOpen(false);
          }}
          data-testid="provider-model"
        />
        {suggestOpen && catalogItems.length > 0 && typeof document !== "undefined"
          ? createPortal(
              <div
                ref={modelPopupRef}
                data-testid="provider-model-options"
                className="fixed z-[60] rounded-md border bg-popover shadow-md"
                onPointerDown={(event) => {
                  event.stopPropagation();
                }}
                onMouseDown={(event) => {
                  event.preventDefault();
                  event.stopPropagation();
                }}
              >
                <ScrollArea viewportClassName="max-h-48">
                  <div>
                    {catalogItems.map((model) => (
                      <button
                        key={model}
                        type="button"
                        className="w-full px-2.5 py-1.5 text-left text-xs hover:bg-accent"
                        onMouseDown={(event) => {
                          event.preventDefault();
                          event.stopPropagation();
                          onModelChange(model);
                          setSuggestOpen(false);
                        }}
                        data-testid={`provider-model-option-${model}`}
                      >
                        {model}
                      </button>
                    ))}
                  </div>
                </ScrollArea>
              </div>,
              document.body,
            )
          : null}
      </div>
      <p className="text-muted-foreground text-xs">{t("admin.providers.modelCatalogHint")}</p>
      {/* Status row reserves a fixed single-line height: showing or hiding the failure hint does not shift layout (loading no longer shows hint copy, owner 2026-11-03),
          so opening the dialog no longer jitters from this line appearing/disappearing (owner 2026-11-03). */}
      <div className="min-h-4">
        {catalogView === "failed" ? (
          <p className="text-muted-foreground text-xs" data-testid="provider-model-catalog-failed">
            {t("admin.providers.modelCatalogFailed")}
          </p>
        ) : null}
      </div>
    </div>
  );
}
