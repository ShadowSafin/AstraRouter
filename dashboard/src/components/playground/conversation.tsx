'use client';

/**
 * The conversation shown above the composer.
 *
 * The composer owns new input, so this surface owns what has already been
 * written: reading the turns that will be sent, and editing them. That split is
 * what makes a run reproducible — the exact message list that produced a result
 * stays visible and editable instead of being replaced by the next keystroke.
 *
 * Roles include `system`, which is now sent rather than dropped: the message
 * list and the system-prompt field are both folded into the wire body.
 */
import { Check, MessageSquarePlus, Pencil, Trash2, X } from 'lucide-react';
import * as React from 'react';

import { Button } from '@/components/ui/button';
import { Select } from '@/components/ui/input';
import type { PlaygroundMessage } from '@/lib/playground';
import { cn } from '@/lib/utils';

const ROLES: Array<PlaygroundMessage['role']> = ['system', 'user', 'assistant', 'tool'];

const ROLE_TONE: Record<PlaygroundMessage['role'], string> = {
  system: 'border-sky-500/30 bg-sky-500/10 text-sky-300',
  user: 'border-emerald-500/30 bg-emerald-500/10 text-emerald-300',
  assistant: 'border-white/[0.1] bg-white/[0.05] text-neutral-300',
  tool: 'border-amber-500/30 bg-amber-500/10 text-amber-300',
};

let messageSeq = 0;

/** A new message with a stable id, so React and replay both keep identity. */
export function newMessage(
  role: PlaygroundMessage['role'] = 'user',
  content = '',
): PlaygroundMessage {
  messageSeq += 1;
  return { id: `msg_${Date.now().toString(36)}_${messageSeq}`, role, content };
}

function RoleBadge({ role }: { role: PlaygroundMessage['role'] }) {
  return (
    <span
      className={cn(
        'rounded-full border px-2 py-px font-mono text-[10px] uppercase tracking-wide',
        ROLE_TONE[role],
      )}
    >
      {role}
    </span>
  );
}

export function Conversation({
  system,
  messages,
  onMessagesChange,
  disabled,
}: {
  system: string;
  messages: PlaygroundMessage[];
  onMessagesChange: (next: PlaygroundMessage[]) => void;
  disabled: boolean;
}) {
  const [editing, setEditing] = React.useState(false);

  const update = (id: string, patch: Partial<PlaygroundMessage>) => {
    onMessagesChange(
      messages.map((message) => (message.id === id ? { ...message, ...patch } : message)),
    );
  };

  const remove = (id: string) => {
    onMessagesChange(messages.filter((message) => message.id !== id));
  };

  return (
    <section className="rounded-2xl border border-white/[0.07] bg-card/95">
      <div className="flex flex-wrap items-center gap-2 border-b border-white/[0.06] px-4 py-2.5">
        <p className="text-[13px] font-medium text-foreground">Conversation</p>
        <span className="text-[11px] text-muted-foreground">
          {messages.length} message{messages.length === 1 ? '' : 's'}
          {system.trim() ? ' · system prompt set' : ''}
        </span>
        <div className="ml-auto flex gap-1">
          <Button
            variant="ghost"
            size="sm"
            onClick={() => onMessagesChange([])}
            disabled={disabled || messages.length === 0}
            title="Empty the conversation and start a fresh run"
          >
            <Trash2 />
            Clear
          </Button>
          <Button
            variant="ghost"
            size="sm"
            onClick={() => setEditing((value) => !value)}
            disabled={disabled && !editing}
          >
            {editing ? <Check /> : <Pencil />}
            {editing ? 'Done' : 'Edit'}
          </Button>
        </div>
      </div>

      <div className={cn('divide-y divide-white/[0.05]', editing ? 'p-2' : 'px-4 py-1')}>
        {messages.length === 0 ? (
          <p className="py-4 text-center text-[12px] text-muted-foreground">
            No messages yet. Type in the composer below to add the first turn.
          </p>
        ) : null}

        {messages.map((message, index) =>
          editing ? (
            <div key={message.id} className="rounded-xl p-2">
              <div className="mb-2 flex items-center gap-2">
                <Select
                  value={message.role}
                  onChange={(event) =>
                    update(message.id, { role: event.target.value as PlaygroundMessage['role'] })
                  }
                  className="h-7 w-28 text-[11px]"
                  aria-label={`Role of message ${index + 1}`}
                >
                  {ROLES.map((role) => (
                    <option key={role} value={role}>
                      {role}
                    </option>
                  ))}
                </Select>
                <span className="flex-1 text-[11px] text-muted-foreground">message {index + 1}</span>
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  className="size-7"
                  aria-label={`Remove message ${index + 1}`}
                  onClick={() => remove(message.id)}
                >
                  <Trash2 />
                </Button>
              </div>
              <textarea
                className="min-h-[64px] w-full resize-y rounded-lg border border-white/[0.09] bg-white/[0.025] px-3 py-2 font-mono text-xs leading-relaxed text-foreground transition-colors placeholder:text-neutral-500 focus-visible:border-primary/60 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-primary/50"
                spellCheck={false}
                placeholder={
                  message.role === 'tool'
                    ? 'Tool result content, if you are replaying a tool turn.'
                    : 'Write the message…'
                }
                value={message.content}
                onChange={(event) => update(message.id, { content: event.target.value })}
              />
            </div>
          ) : (
            <div
              key={message.id}
              className="group flex items-start gap-2.5 py-2 transition-colors hover:bg-white/[0.015]"
            >
              <RoleBadge role={message.role} />
              <p
                className={cn(
                  'min-w-0 flex-1 whitespace-pre-wrap break-words text-[12px] leading-relaxed',
                  message.content.trim() ? 'text-neutral-300' : 'text-danger',
                )}
              >
                {message.content.trim() ? (
                  message.content
                ) : (
                  <em>empty — this will block the run</em>
                )}
              </p>
              <div className="flex shrink-0 gap-1 opacity-0 transition-opacity focus-within:opacity-100 group-hover:opacity-100">
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  className="size-6"
                  aria-label={`Edit message ${index + 1}`}
                  onClick={() => setEditing(true)}
                >
                  <Pencil />
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  className="size-6"
                  aria-label={`Remove message ${index + 1}`}
                  disabled={disabled}
                  onClick={() => remove(message.id)}
                >
                  <Trash2 />
                </Button>
              </div>
            </div>
          ),
        )}
      </div>

      {editing ? (
        <div className="flex flex-wrap gap-1.5 border-t border-white/[0.06] p-3">
          <p className="mr-1 self-center text-[11px] text-muted-foreground">Add turn</p>
          {ROLES.map((role) => (
            <Button
              key={role}
              type="button"
              variant="outline"
              size="sm"
              onClick={() => onMessagesChange([...messages, newMessage(role)])}
            >
              {role === 'user' ? <MessageSquarePlus /> : null}
              {role}
            </Button>
          ))}
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="ml-auto"
            onClick={() => setEditing(false)}
          >
            <X />
            Close
          </Button>
        </div>
      ) : null}
    </section>
  );
}
