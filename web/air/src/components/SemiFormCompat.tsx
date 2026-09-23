import type { ChangeEvent, ComponentProps, ComponentType, ReactNode } from 'react';
import { DatePicker, Form, Input, TextArea } from '@douyinfe/semi-ui';

type FormFieldExtras = {
  field?: string;
  name?: string;
  label?: ReactNode;
  fluid?: boolean;
  loading?: boolean;
  icon?: ReactNode;
  iconPosition?: string;
  initValue?: string;
  action?: ReactNode;
};

type InputChange =
  | ((value: string, event: ChangeEvent<HTMLInputElement>) => void)
  | ((event: ChangeEvent<HTMLInputElement>, data: { name?: string; value: string }) => void);

type LegacyInputProps = Omit<ComponentProps<typeof Input>, 'onChange'> & FormFieldExtras & {
  onChange?: InputChange;
};

function forwardInputChange(onChange: InputChange | undefined, name: string | undefined) {
  return (value: string, event: ChangeEvent<HTMLInputElement>) => {
    if (!onChange) return;
    if (onChange.length > 1) {
      (onChange as (event: ChangeEvent<HTMLInputElement>, data: { name?: string; value: string }) => void)(event, { name, value });
    } else {
      (onChange as (value: string, event: ChangeEvent<HTMLInputElement>) => void)(value, event);
    }
  };
}

// Semi's Form fields support controlled values at runtime. Their current
// declarations omit those props, so keep the existing form behavior while
// using the underlying control's public prop types.
const SemiFormInput = Form.Input as ComponentType<ComponentProps<typeof Input> & FormFieldExtras>;

export function LegacyFormInput({ onChange, name, ...props }: LegacyInputProps) {
  return <SemiFormInput {...props} name={name} onChange={forwardInputChange(onChange, name)} />;
}

export const LegacyFormDatePicker = Form.DatePicker as ComponentType<ComponentProps<typeof DatePicker> & FormFieldExtras>;

export function LegacyInput({ onChange, name, ...props }: LegacyInputProps) {
  return <Input {...props} name={name} onChange={forwardInputChange(onChange, name)} />;
}

export const LegacyDatePicker = DatePicker as ComponentType<ComponentProps<typeof DatePicker> & FormFieldExtras>;
export const LegacyTextArea = TextArea as ComponentType<ComponentProps<typeof TextArea> & FormFieldExtras>;
