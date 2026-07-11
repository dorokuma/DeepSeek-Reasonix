import { Bot, WandSparkles, X } from "lucide-react";
import type { InvocationDisplay } from "../lib/invocationDisplay";
import { useT } from "../lib/i18n";
import { Tooltip } from "./Tooltip";

export function InvocationBadge({
  invocation,
  kind = "skill",
  description,
  onRemove,
  variant,
}: {
  invocation: InvocationDisplay;
  kind?: "skill" | "subagent";
  description?: string;
  onRemove?: () => void;
  variant: "composer" | "message";
}) {
  const t = useT();
  return (
    <span className={`invocation-display invocation-display--${variant} invocation-display--${kind}`} role="group" aria-label={t("composer.selectedInvocation")}>
      <Tooltip label={description || `/${invocation.name}`}>
        <span className="invocation-display__identity">
          {kind === "subagent"
            ? <Bot size={variant === "composer" ? 18 : 16} />
            : <WandSparkles size={variant === "composer" ? 18 : 16} />}
          <span className="invocation-display__name">{invocation.label}</span>
          {invocation.source && <span className="invocation-display__source">{t("slash.plugin", { name: invocation.source })}</span>}
        </span>
      </Tooltip>
      {onRemove && (
        <Tooltip label={t("composer.removeInvocation")}>
          <button
            type="button"
            className="invocation-display__remove"
            onClick={onRemove}
            aria-label={t("composer.removeInvocationNamed", { name: invocation.label })}
          >
            <X size={14} />
          </button>
        </Tooltip>
      )}
    </span>
  );
}
