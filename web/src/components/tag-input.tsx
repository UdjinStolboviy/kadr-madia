import { X } from "lucide-react";
import { useId, useState, type KeyboardEvent } from "react";

import { cn } from "@/lib/utils";

/** Free-form tag input: Enter, comma or space commits a value; Backspace removes the last one. */
export function TagInput({
  value,
  onChange,
  placeholder,
  validate,
  id,
  invalid,
}: {
  value: string[];
  onChange: (next: string[]) => void;
  placeholder?: string;
  /** Return an error message to reject a tag. */
  validate?: (tag: string) => string | null;
  id?: string;
  invalid?: boolean;
}) {
  const [draft, setDraft] = useState("");
  const [error, setError] = useState<string | null>(null);
  const autoId = useId();
  const inputId = id ?? autoId;

  function commit(raw: string): boolean {
    const tags = raw
      .split(/[\s,]+/)
      .map((t) => t.trim())
      .filter(Boolean);
    if (!tags.length) return true;
    const next = [...value];
    for (const t of tags) {
      const err = validate?.(t) ?? null;
      if (err) {
        setError(err);
        return false;
      }
      if (!next.includes(t)) next.push(t);
    }
    setError(null);
    onChange(next);
    setDraft("");
    return true;
  }

  function onKeyDown(e: KeyboardEvent<HTMLInputElement>) {
    if (e.key === "Enter" || e.key === "," || (e.key === " " && draft.trim())) {
      if (draft.trim()) {
        e.preventDefault();
        commit(draft);
      } else if (e.key !== "Enter") {
        e.preventDefault();
      }
    } else if (e.key === "Backspace" && !draft && value.length) {
      onChange(value.slice(0, -1));
    }
  }

  return (
    <div>
      <div
        className={cn(
          "flex min-h-9 w-full flex-wrap items-center gap-1.5 rounded-md border border-input bg-transparent px-2 py-1.5 text-sm shadow-xs transition-[color,box-shadow] focus-within:border-ring focus-within:ring-[3px] focus-within:ring-ring/50 dark:bg-input/30",
          (invalid || error) && "border-destructive ring-destructive/20",
        )}
        onClick={() => document.getElementById(inputId)?.focus()}
      >
        {value.map((tag) => (
          <span
            key={tag}
            className="inline-flex max-w-full items-center gap-1 rounded-md bg-secondary px-2 py-0.5 font-mono text-xs text-secondary-foreground"
          >
            <span className="truncate">{tag}</span>
            <button
              type="button"
              className="rounded-sm text-muted-foreground hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring"
              aria-label={`Remove ${tag}`}
              onClick={(e) => {
                e.stopPropagation();
                onChange(value.filter((t) => t !== tag));
              }}
            >
              <X className="size-3" />
            </button>
          </span>
        ))}
        <input
          id={inputId}
          value={draft}
          onChange={(e) => {
            setDraft(e.target.value);
            if (error) setError(null);
          }}
          onKeyDown={onKeyDown}
          onBlur={() => draft.trim() && commit(draft)}
          onPaste={(e) => {
            const text = e.clipboardData.getData("text");
            if (/[\s,]/.test(text)) {
              e.preventDefault();
              commit(draft + text);
            }
          }}
          placeholder={value.length ? "" : placeholder}
          className="min-w-[8rem] flex-1 bg-transparent py-0.5 outline-none placeholder:text-muted-foreground"
        />
      </div>
      {error && (
        <p className="mt-1.5 text-xs text-destructive" role="alert">
          {error}
        </p>
      )}
    </div>
  );
}
