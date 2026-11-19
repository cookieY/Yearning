import { useState, type DragEvent } from "react";
import { useTranslation } from "react-i18next";
import { Card, CardContent, CardHeader, CardTitle } from "@/shared/components/ui/card";
import { CloudUpload } from "lucide-react";

export interface SqlFileDropzoneProps {
  /** Click opens the import dialog (its own file picker and validation). */
  onOpen: () => void;
  /** A file dropped on the zone: handed to the import dialog for the same
   * incremental read and 32 MiB / 512 KiB pre-validation as a picked file. */
  onDropFile: (file: File) => void;
  disabled?: boolean;
}

/** SQL file upload card (reference: Shadcn Dashboard file-upload-01 Dropzone):
 * title, dashed drop area with the cloud-upload icon and the drop prompt,
 * centred hint underneath. The zone is a real button so keyboard and
 * disabled semantics come for free. */
export function SqlFileDropzone({ onOpen, onDropFile, disabled = false }: SqlFileDropzoneProps) {
  const { t } = useTranslation();
  const [dragging, setDragging] = useState(false);

  const handleDrop = (event: DragEvent<HTMLButtonElement>): void => {
    event.preventDefault();
    setDragging(false);
    if (disabled) return;
    const file = event.dataTransfer.files[0];
    if (file !== undefined) onDropFile(file);
  };

  return (
    <Card className="[--card-spacing:--spacing(4)]" data-testid="sql-file-dropzone">
      <CardHeader>
        <CardTitle>
          <h5 className="text-base font-semibold">{t("precheck.bulk.import.action")}</h5>
        </CardTitle>
      </CardHeader>
      <CardContent>
        <button
          type="button"
          disabled={disabled}
          onClick={onOpen}
          onDragOver={(event) => {
            event.preventDefault();
            if (!disabled) setDragging(true);
          }}
          onDragLeave={() => { setDragging(false); }}
          onDrop={handleDrop}
          data-testid="open-bulk-import"
          className={
            "flex h-40 w-full flex-col items-center justify-center gap-4 rounded-lg border border-dashed bg-muted/30 px-4 text-sm font-semibold transition-colors disabled:pointer-events-none disabled:opacity-50 " +
            (dragging ? "border-primary bg-primary/5" : "border-foreground/40 hover:border-foreground/70")
          }
        >
          <CloudUpload className="size-8" aria-hidden />
          {t("precheck.bulk.import.dropTitle")}
        </button>
        <p className="text-muted-foreground mt-4 text-center text-xs">
          {t("precheck.bulk.import.dropHint")}
        </p>
      </CardContent>
    </Card>
  );
}
