import { EyeOutlined } from '@ant-design/icons';
import { Tooltip } from 'antd';
import type { JSX } from 'react';

interface Props {
  onClick: () => void;
  className?: string;
}

// The system project is hidden from project lists so people don't mistake it
// for one of theirs; admins open it on purpose from here.
export const SystemProjectLinkComponent = ({ onClick, className = '' }: Props): JSX.Element => (
  <Tooltip
    placement="right"
    title="Obzly's own sampled request traces, for troubleshooting Obzly itself. Visible to admins only."
  >
    <button
      type="button"
      onClick={onClick}
      className={`hover:text-accent-600 dark:hover:text-accent-400 flex w-full cursor-pointer items-center gap-2 text-left text-xs text-gray-500 dark:text-gray-400 ${className}`}
    >
      <EyeOutlined />
      View system project
    </button>
  </Tooltip>
);
