import { LoadingOutlined, PlusOutlined } from '@ant-design/icons';
import { AutoComplete, DatePicker, Input, Select, Spin, Tag } from 'antd';
import dayjs from 'dayjs';
import React, { useEffect, useMemo, useState } from 'react';

import type { ConditionNode, QueryOperator, QueryableField } from '../../../entity/query';
import { getUserShortTimeFormat } from '../../../shared/time';

const { Option } = Select;

interface Props {
  fields: QueryableField[];
  condition?: ConditionNode;
  onChange: (condition: ConditionNode) => void;
  onFieldSearch?: (searchTerm?: string) => Promise<QueryableField[]>;
}

export const ConditionEditorComponent = ({
  fields,
  condition,
  onChange,
  onFieldSearch,
}: Props): React.JSX.Element => {
  const [arrayValues, setArrayValues] = useState<string[]>([]);
  const [inputValue, setInputValue] = useState('');
  const [localFields, setLocalFields] = useState<QueryableField[]>(fields);
  const [isLocalSearching, setIsLocalSearching] = useState(false);
  const [searchTimeout, setSearchTimeout] = useState<ReturnType<typeof setTimeout> | null>(null);

  const timeFormat = useMemo(() => getUserShortTimeFormat(), []);

  const debouncedSearchFields = async (searchTerm?: string) => {
    if (searchTimeout) {
      clearTimeout(searchTimeout);
    }

    setIsLocalSearching(true);

    const timeoutId = setTimeout(async () => {
      if (onFieldSearch && searchTerm && searchTerm.length > 2) {
        try {
          const searchResults = await onFieldSearch(searchTerm);
          setLocalFields(searchResults);
        } catch (error) {
          console.error('Field search failed:', error);
          setLocalFields(fields);
        }
      } else {
        setLocalFields(fields);
      }
      setIsLocalSearching(false);
    }, 250);

    setSearchTimeout(timeoutId);
  };

  const getOrCreateField = (fieldName: string): QueryableField => {
    const existingField = fields.find((f) => f.name === fieldName);
    if (existingField) {
      return existingField;
    }

    return {
      name: fieldName,
      type: 'string',
      operations: [
        'equals',
        'not_equals',
        'contains',
        'not_contains',
        'exists',
        'not_exists',
      ] as QueryOperator[],
      isCustom: true,
    };
  };

  const getDefaultValueForOperator = (
    operator: QueryOperator,
  ): string | number | boolean | string[] | null => {
    switch (operator) {
      case 'exists':
      case 'not_exists':
        return null;
      case 'in':
      case 'not_in':
        return [];
      default:
        return '';
    }
  };

  const operatorNeedsValue = (operator: QueryOperator): boolean => {
    return operator !== 'exists' && operator !== 'not_exists';
  };

  const operatorExpectsArray = (operator: QueryOperator): boolean => {
    return operator === 'in' || operator === 'not_in';
  };

  const isTimestampField = (fieldName: string): boolean => {
    return fieldName === 'timestamp' || fieldName === 'created_at';
  };

  const handleFieldChange = (fieldName: string) => {
    if (!fieldName || fieldName.trim() === '') {
      onChange({
        field: '',
        operator: 'equals',
        value: '',
      });
      return;
    }

    const field = getOrCreateField(fieldName);
    const currentOperator = condition?.operator;
    const currentValue = condition?.value;

    let newOperator: QueryOperator;
    if (currentOperator && field.operations.includes(currentOperator)) {
      newOperator = currentOperator;
    } else {
      // "contains" is the more common choice for message.
      newOperator =
        fieldName === 'message' && field.operations.includes('contains')
          ? 'contains'
          : field.operations[0];
    }

    // Keep the current value unless the new operator can't take it.
    let newValue: string | number | boolean | string[] | null;

    const hasValue =
      currentValue !== null &&
      currentValue !== undefined &&
      currentValue !== '' &&
      !(Array.isArray(currentValue) && currentValue.length === 0);

    if (!hasValue) {
      newValue = getDefaultValueForOperator(newOperator);
    } else {
      if (!operatorNeedsValue(newOperator)) {
        newValue = null;
      } else if (operatorExpectsArray(newOperator) && !Array.isArray(currentValue)) {
        newValue = [String(currentValue)];
      } else if (!operatorExpectsArray(newOperator) && Array.isArray(currentValue)) {
        newValue = currentValue.length > 0 ? String(currentValue[0]) : '';
      } else {
        newValue = currentValue;
      }
    }

    onChange({
      field: fieldName,
      operator: newOperator,
      value: newValue,
    });
  };

  const handleOperatorChange = (operator: QueryOperator) => {
    const currentValue = condition?.value;
    let newValue = currentValue;

    // Convert the value only when the old one can't fit the new operator.
    if (!operatorNeedsValue(operator)) {
      newValue = null;
    } else if (operatorExpectsArray(operator) && !Array.isArray(currentValue)) {
      newValue = currentValue && currentValue !== '' ? [String(currentValue)] : [];
    } else if (!operatorExpectsArray(operator) && Array.isArray(currentValue)) {
      newValue = currentValue.length > 0 ? String(currentValue[0]) : '';
    } else if (newValue === null || newValue === undefined) {
      newValue = getDefaultValueForOperator(operator);
    }

    onChange({
      field: condition?.field || currentField?.name || '',
      operator,
      value: newValue,
    });
  };

  const handleValueChange = (value: string | number | boolean | string[] | null) => {
    onChange({
      field: condition?.field || currentField?.name || '',
      operator: currentOperator,
      value,
    });
  };

  const handleArrayValueAdd = () => {
    if (inputValue.trim()) {
      const newValues = [...arrayValues, inputValue.trim()];
      setArrayValues(newValues);
      setInputValue('');
      handleValueChange(newValues);
    }
  };

  const handleArrayValueRemove = (index: number) => {
    const newValues = arrayValues.filter((_, i) => i !== index);
    setArrayValues(newValues);
    handleValueChange(newValues);
  };

  const renderValueInput = () => {
    if (!operatorNeedsValue(currentOperator)) {
      return null;
    }

    if (operatorExpectsArray(currentOperator)) {
      return (
        <div className="space-y-2">
          <div className="flex space-x-2">
            <Input
              placeholder="Enter value"
              value={inputValue}
              onChange={(e) => setInputValue(e.target.value)}
              onPressEnter={handleArrayValueAdd}
              size="small"
              className="flex-1"
            />
            <button
              type="button"
              onClick={handleArrayValueAdd}
              className="rounded bg-emerald-600 px-2 py-1 text-sm text-white hover:bg-emerald-700"
            >
              <PlusOutlined />
            </button>
          </div>

          {arrayValues.length > 0 && (
            <div className="flex flex-wrap gap-1">
              {arrayValues.map((value, index) => (
                <Tag
                  key={index}
                  closable
                  onClose={() => handleArrayValueRemove(index)}
                  className="border-emerald-200 bg-emerald-50"
                >
                  {value}
                </Tag>
              ))}
            </div>
          )}
        </div>
      );
    }

    if (
      isTimestampField(condition?.field || '') &&
      [
        'greater_than',
        'greater_or_equal',
        'less_than',
        'less_or_equal',
        'equals',
        'not_equals',
      ].includes(currentOperator)
    ) {
      return (
        <DatePicker
          showTime={{
            format: timeFormat.use12Hours ? 'h:mm A' : 'HH:mm',
            use12Hours: timeFormat.use12Hours,
          }}
          format={timeFormat.format}
          value={condition?.value ? dayjs(condition.value as string) : null}
          onChange={(date) => {
            handleValueChange(date ? date.toISOString() : '');
          }}
          placeholder="Select date and time"
          size="small"
          className="w-full"
        />
      );
    }

    return (
      <Input
        placeholder="Enter value"
        value={(condition?.value as string) || ''}
        onChange={(e) => handleValueChange(e.target.value)}
        size="small"
      />
    );
  };

  const currentField =
    condition?.field && condition.field.trim() !== ''
      ? getOrCreateField(condition.field)
      : getOrCreateField('');
  const currentOperator = condition?.operator || currentField?.operations[0] || 'equals';

  const operatorDisplayNames: Record<QueryOperator, string> = {
    equals: 'equals',
    not_equals: 'not equals',
    contains: 'contains',
    not_contains: 'does not contain',
    in: 'is in',
    not_in: 'is not in',
    greater_than: 'greater than',
    greater_or_equal: 'greater than or equal',
    less_than: 'less than',
    less_or_equal: 'less than or equal',
    exists: 'exists',
    not_exists: 'does not exist',
  };

  const fieldOptions = (() => {
    const options = localFields.map((field) => ({
      value: field.name,
      label: (
        <div className="flex items-center justify-between">
          <span>{field.name}</span>
          <span className="text-xs text-gray-400 dark:text-gray-400">{field.type}</span>
        </div>
      ),
    }));

    if (!condition?.field || condition.field.trim() === '') {
      const messageIndex = options.findIndex((option) => option.value === 'message');
      if (messageIndex > 0) {
        const messageOption = options.splice(messageIndex, 1)[0];
        options.unshift(messageOption);
      }
    }

    return options;
  })();

  useEffect(() => {
    if (
      operatorExpectsArray(currentOperator) &&
      Array.isArray(condition?.value) &&
      arrayValues.length === 0
    ) {
      setArrayValues(condition.value.map(String));
    }

    if (!operatorExpectsArray(currentOperator) && arrayValues.length > 0) {
      setArrayValues([]);
    }
  }, [currentOperator, condition?.value, arrayValues.length]);

  useEffect(() => {
    setLocalFields(fields);
  }, [fields]);

  useEffect(() => {
    return () => {
      if (searchTimeout) {
        clearTimeout(searchTimeout);
      }
    };
  }, [searchTimeout]);

  return (
    <div className="space-y-3">
      <div className="grid grid-cols-12 items-center gap-2">
        <div className="col-span-4">
          <label className="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-300">
            Field
          </label>
          <div className="relative">
            <AutoComplete
              value={condition?.field || ''}
              onChange={handleFieldChange}
              onSearch={debouncedSearchFields}
              options={fieldOptions}
              placeholder="Type or select field name"
              size="small"
              className="w-full"
              allowClear
              backfill={false}
              notFoundContent={null}
              filterOption={false}
            />
            {isLocalSearching && (
              <div className="absolute top-1/2 right-2 -translate-y-1/2">
                <Spin indicator={<LoadingOutlined spin style={{ fontSize: 14 }} />} />
              </div>
            )}
          </div>
        </div>

        <div className="col-span-3">
          <label className="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-300">
            Operator
          </label>
          <Select
            value={currentOperator}
            onChange={handleOperatorChange}
            size="small"
            className="w-full"
          >
            {currentField?.operations.map((op) => (
              <Option key={op} value={op}>
                {operatorDisplayNames[op]}
              </Option>
            ))}
          </Select>
        </div>

        <div className="col-span-5">
          {operatorNeedsValue(currentOperator) && (
            <>
              <label className="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-300">
                Value
              </label>
              {renderValueInput()}
            </>
          )}

          {!operatorNeedsValue(currentOperator) && (
            <div className="pt-4 text-xs text-gray-400 dark:text-gray-400">No value needed</div>
          )}
        </div>
      </div>

      <div className="flex items-center justify-between text-xs text-gray-500 dark:text-gray-300">
        <div>
          <span className="font-medium">{currentField?.type}</span> field{' '}
          {currentField?.type === 'string' ? '(case-sensitive)' : ''}
        </div>
      </div>
    </div>
  );
};
