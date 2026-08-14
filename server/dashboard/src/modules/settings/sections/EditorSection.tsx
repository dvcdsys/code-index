import { useState } from 'react';
import { Card, CardBody, CardHead } from '@/ui/card';
import { RadioCard, RadioGroup } from '@/ui/radio-group';
import {
  getEditorPreference,
  setEditorPreference,
  type EditorProtocol,
} from '@/lib/editorPreference';

const OPTIONS: ReadonlyArray<{ value: EditorProtocol; label: string; hint: string }> = [
  {
    value: 'cursor',
    label: 'Cursor',
    hint: 'cursor:// — falls back to VS Code if Cursor is not installed',
  },
  { value: 'vscode', label: 'VS Code', hint: 'vscode:// directly' },
  { value: 'none', label: 'Disabled', hint: 'the open link does nothing' },
];

export function EditorSection() {
  const [pref, setPref] = useState<EditorProtocol>(() => getEditorPreference());

  function onChange(next: EditorProtocol) {
    setPref(next);
    setEditorPreference(next);
  }

  return (
    <Card>
      <CardHead
        title="Open in editor"
        aside={<span className="font-mono text-[11px] font-normal text-muted">this browser</span>}
      />
      <CardBody>
        <p className="mb-3.5 mt-0 text-[13.5px] text-dim">
          What the open link next to a search result does.
        </p>
        <RadioGroup value={pref} onValueChange={(v) => onChange(v as EditorProtocol)}>
          {OPTIONS.map((o) => (
            <RadioCard
              key={o.value}
              id={`editor-${o.value}`}
              value={o.value}
              selected={pref === o.value}
              title={o.label}
              hint={o.hint}
            />
          ))}
        </RadioGroup>
      </CardBody>
    </Card>
  );
}
