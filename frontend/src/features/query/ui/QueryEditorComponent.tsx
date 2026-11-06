import { CloseCircleFilled } from '@ant-design/icons';
import { autocompletion, closeBrackets, completionStatus } from '@codemirror/autocomplete';
import { defaultKeymap, history, historyKeymap } from '@codemirror/commands';
import { Compartment, EditorState, type Extension, Prec } from '@codemirror/state';
import { EditorView, keymap, placeholder as placeholderExt } from '@codemirror/view';
import { type JSX, useEffect, useLayoutEffect, useRef } from 'react';

import type { QueryableField } from '../../../entity/query';
import { type ValueSuggestion, searchLanguage } from '../lib/queryEditor/searchLanguage';
import { sqlLanguage } from '../lib/queryEditor/sqlLanguage';

interface Props {
  mode: 'search' | 'sql';
  value: string;
  onChange: (value: string) => void;
  /** Enter (when no completion is open). Shift+Enter adds a line in SQL mode. */
  onSubmit: () => void;
  placeholder: string;
  fields: QueryableField[];
  loadValues?: (field: string) => Promise<ValueSuggestion[]>;
  hasError?: boolean;
  isDark: boolean;
}

const MONO = 'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace';

const baseTheme = EditorView.theme({
  '&': { fontSize: '13px', minHeight: '32px', flex: '1' },
  '&.cm-focused': { outline: 'none' },
  '.cm-scroller': {
    fontFamily: MONO,
    lineHeight: '20px',
  },
  '.cm-content': { padding: '5px 0', fontFamily: MONO },
  '.cm-tooltip-autocomplete ul': { fontFamily: MONO },
  '.cm-line': { padding: '0 4px' },
  '.cm-tooltip': { borderRadius: '6px', fontSize: '12px' },
  '.cm-tooltip-autocomplete ul li[aria-selected]': { background: '#059669', color: '#fff' },
});

const darkTheme = EditorView.theme(
  {
    '&': { color: '#e5e7eb', backgroundColor: 'transparent' },
    '.cm-cursor': { borderLeftColor: '#e5e7eb' },
    '.cm-tooltip': { backgroundColor: '#1f2937', color: '#e5e7eb', border: '1px solid #374151' },
    '.cm-placeholder': { color: '#6b7280' },
  },
  { dark: true },
);

const lightTheme = EditorView.theme({
  '.cm-tooltip': { backgroundColor: '#fff', border: '1px solid #e5e7eb' },
  '.cm-placeholder': { color: '#9ca3af' },
});

// Search mode is one line: newlines typed or pasted become spaces.
const singleLine = EditorState.transactionFilter.of((tr) => {
  if (!tr.docChanged || !tr.newDoc.toString().includes('\n')) return tr;
  const changes: { from: number; to: number; insert: string }[] = [];
  tr.changes.iterChanges((fromA, toA, _fromB, _toB, inserted) => {
    changes.push({ from: fromA, to: toA, insert: inserted.toString().replace(/\r?\n/g, ' ') });
  });
  return [{ changes, selection: tr.selection, scrollIntoView: tr.scrollIntoView }];
});

/**
 * CodeMirror-based query input with highlighting, completion, hover help and
 * inline hints - the search syntax or DuckDB SQL over the logs table.
 */
export const QueryEditorComponent = ({
  mode,
  value,
  onChange,
  onSubmit,
  placeholder,
  fields,
  loadValues,
  hasError,
  isDark,
}: Props): JSX.Element => {
  const hostRef = useRef<HTMLDivElement>(null);
  const viewRef = useRef<EditorView | null>(null);
  const languageCompartment = useRef(new Compartment());
  const themeCompartment = useRef(new Compartment());
  const placeholderCompartment = useRef(new Compartment());

  // Latest props for callbacks captured by the editor.
  const latest = useRef({ onChange, onSubmit, fields, loadValues });
  useLayoutEffect(() => {
    latest.current = { onChange, onSubmit, fields, loadValues };
  });

  const languageFor = (editorMode: Props['mode']): Extension => {
    const getFields = () => latest.current.fields;
    return editorMode === 'sql'
      ? sqlLanguage(getFields)
      : [
          singleLine,
          searchLanguage({
            getFields,
            loadValues: (field) => latest.current.loadValues?.(field) ?? Promise.resolve([]),
          }),
        ];
  };

  useEffect(() => {
    if (!hostRef.current) return;
    const submitKeymap = Prec.high(
      keymap.of([
        {
          key: 'Enter',
          run: (view) => {
            if (completionStatus(view.state) === 'active') return false;
            latest.current.onSubmit();
            return true;
          },
        },
      ]),
    );

    const view = new EditorView({
      parent: hostRef.current,
      state: EditorState.create({
        doc: value,
        extensions: [
          history(),
          closeBrackets(),
          autocompletion({ activateOnTyping: true, icons: false }),
          submitKeymap,
          keymap.of([...defaultKeymap, ...historyKeymap]),
          baseTheme,
          themeCompartment.current.of(isDark ? darkTheme : lightTheme),
          languageCompartment.current.of(languageFor(mode)),
          placeholderCompartment.current.of(placeholderExt(placeholder)),
          EditorView.contentAttributes.of({ 'aria-label': 'Log query', spellcheck: 'false' }),
          EditorView.updateListener.of((update) => {
            if (update.docChanged) latest.current.onChange(update.state.doc.toString());
          }),
        ],
      }),
    });
    viewRef.current = view;
    return () => {
      view.destroy();
      viewRef.current = null;
    };
    // The editor is created once; props flow in through the effects below.
  }, []);

  useEffect(() => {
    viewRef.current?.dispatch({
      effects: languageCompartment.current.reconfigure(languageFor(mode)),
    });
  }, [mode]);

  useEffect(() => {
    viewRef.current?.dispatch({
      effects: themeCompartment.current.reconfigure(isDark ? darkTheme : lightTheme),
    });
  }, [isDark]);

  useEffect(() => {
    viewRef.current?.dispatch({
      effects: placeholderCompartment.current.reconfigure(placeholderExt(placeholder)),
    });
  }, [placeholder]);

  // External edits (click-to-filter, chips, recent/saved searches).
  useEffect(() => {
    const view = viewRef.current;
    if (!view || view.state.doc.toString() === value) return;
    view.dispatch({
      changes: { from: 0, to: view.state.doc.length, insert: value },
      selection: { anchor: value.length },
    });
  }, [value]);

  const clear = () => {
    const view = viewRef.current;
    if (!view) return;
    view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: '' } });
    view.focus();
  };

  return (
    <div
      className={`flex min-w-[280px] flex-1 items-start rounded-md border bg-white px-1 transition-colors focus-within:border-emerald-500 dark:bg-transparent ${
        hasError
          ? 'border-red-400 dark:border-red-500'
          : 'border-gray-300 hover:border-emerald-400 dark:border-gray-600'
      }`}
    >
      <div ref={hostRef} className="min-w-0 flex-1" data-testid="query-editor" />
      {mode === 'search' && value && (
        <button
          type="button"
          aria-label="Clear search"
          onClick={clear}
          className="mt-[7px] mr-1 cursor-pointer text-gray-300 hover:text-gray-500 dark:text-gray-500 dark:hover:text-gray-300"
        >
          <CloseCircleFilled />
        </button>
      )}
    </div>
  );
};
