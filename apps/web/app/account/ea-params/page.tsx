"use client";

import { Suspense } from "react";
import { useSearchParams } from "next/navigation";
import { CustomEAParamsPanel } from "../../components/CustomEAParamsPanel";
import { SiteFrame } from "../../components/SiteFrame";

function ParamsContent() {
  const accountId = useSearchParams().get("account") || "";
  return /^\d+$/.test(accountId) ? <CustomEAParamsPanel accountId={accountId} /> : <p className="muted">Akun tidak ditemukan.</p>;
}

export default function CustomEAParamsPage() {
  return <SiteFrame><Suspense fallback={null}><ParamsContent /></Suspense></SiteFrame>;
}
