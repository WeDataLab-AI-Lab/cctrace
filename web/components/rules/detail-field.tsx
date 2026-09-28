interface DetailFieldProps {
  label: string;
  value: string;
}

const DetailField = ({ label, value }: DetailFieldProps) => {
  return (
    <div className="min-w-0 rounded-md border border-border bg-canvas px-3 py-2">
      <p className="text-[10px] font-medium uppercase text-ink-3">{label}</p>
      <p className="mt-1 truncate text-[12px] text-ink">{value}</p>
    </div>
  );
};

export { DetailField };
