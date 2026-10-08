"use client";

import { api } from "@/lib/api";
import { useAsync } from "@/lib/hooks";
import { AccountPicker, accountMode } from "./post-editor";
import { useMeta } from "./providers";
import { Field } from "./ui";
import { Select } from "./select";

/**
 * Where drafts made for a task (or a schedule's tasks) are published: a publisher and, for
 * per-account publishers such as the MCP publisher, the accounts. Drafts start with these; the reviewer
 * can still change them.
 */
export function PublishTarget({
  publisherId,
  accounts,
  onChange,
  disabled,
}: {
  publisherId: string | null;
  accounts: string[];
  onChange: (publisherId: string | null, accounts: string[]) => void;
  disabled?: boolean;
}) {
  const { meta } = useMeta();
  const publishers = useAsync(() => api.listConnectors("publisher"), []);
  const list = publishers.data ?? [];
  const selected = list.find((c) => c.id === publisherId);
  const mode = accountMode(selected, meta?.connector_types ?? []);

  return (
    <div className="space-y-3">
      <Field
        label="Publish drafts to"
        help="Drafts start with this publisher and these accounts; you can still change them in Review."
      >
        <Select disabled={disabled} value={publisherId ?? ""} onChange={(e) => onChange(e.target.value || null, [])}>
          <option value="">Choose when reviewing</option>
          {list.map((c) => (
            <option key={c.id} value={c.id}>
              {c.name}
              {!c.is_active ? " (inactive)" : ""}
            </option>
          ))}
        </Select>
      </Field>
      {selected && mode && (
        <AccountPicker
          connector={selected}
          required={false}
          help="Each draft gets the picked accounts on its platform (a Threads draft gets only the Threads accounts)."
          platforms={meta?.platforms ?? []}
          value={accounts}
          readOnly={disabled}
          onChange={(next) => onChange(publisherId, next)}
        />
      )}
    </div>
  );
}
