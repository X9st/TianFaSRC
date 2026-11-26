"use client";

import { useEffect, useState } from "react";

type EvidenceEditorProps = {
  role: string;
  note: string;
  position: number;
  total: number;
  busy: boolean;
  onSave: (role: string, note: string) => void;
  onMove: (direction: -1 | 1) => void;
};

export default function EvidenceEditor({ role, note, position, total, busy, onSave, onMove }: EvidenceEditorProps) {
  const [nextRole, setNextRole] = useState(role);
  const [nextNote, setNextNote] = useState(note);
  useEffect(() => { setNextRole(role); setNextNote(note); }, [role, note]);

  return <div className="phase5-evidence-editor">
    <label>用途<select value={nextRole} onChange={(event) => setNextRole(event.target.value)}>
      <option value="baseline">基线</option><option value="proof">漏洞证明</option><option value="verification">复测</option><option value="supporting">辅助</option>
    </select></label>
    <label>说明<input value={nextNote} maxLength={4000} onChange={(event) => setNextNote(event.target.value)} placeholder="说明这份证据证明了什么" /></label>
    <div className="workspace-actions">
      <button type="button" className="workspace-secondary" disabled={busy || position === 0} onClick={() => onMove(-1)}>上移</button>
      <button type="button" className="workspace-secondary" disabled={busy || position === total - 1} onClick={() => onMove(1)}>下移</button>
      <button type="button" className="workspace-primary" disabled={busy || (role === nextRole && note === nextNote)} onClick={() => onSave(nextRole, nextNote)}>保存说明</button>
    </div>
  </div>;
}
