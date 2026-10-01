/**
 * Plain-English explanations for the codes Kifaru shows, written for someone
 * with no fraud background. The codes stay visible for specialists.
 */
export interface Explanation {
  title: string;
  detail: string;
}

const RISK_CODES: Record<string, Explanation> = {
  "IP-401": {
    title: "One device used by unrelated customers",
    detail: "The same phone or computer accessed several unrelated customers' accounts. Fraudsters often work through many victims from one device.",
  },
  "IP-402": {
    title: "SIM card swapped just before the payment",
    detail: "The customer's SIM card was replaced in the last few days. Fraudsters swap SIM cards so the victim's one-time PINs come to them.",
  },
  "IP-403": {
    title: "Emulator or tampered phone",
    detail: "The app ran on a simulated or rooted (tampered) phone, which fraud tools often use.",
  },
  "IP-404": {
    title: "Connected from abroad or through a VPN",
    detail: "The session came from an unusual country or through a VPN, proxy or Tor, which hides where the person really is.",
  },
  "VEL-429": {
    title: "Many transfers within an hour",
    detail: "An unusual burst of transfers in a short time, typical of someone emptying an account quickly.",
  },
  "VEL-430": {
    title: "New recipient paid within minutes",
    detail: "A new recipient was added and paid almost at once, before the real customer could notice.",
  },
  "VEL-431": {
    title: "Payments kept just under limits",
    detail: "Amounts were kept just below the level that triggers extra checks, a pattern called structuring.",
  },
  "MUL-440": {
    title: "New account receiving money from many strangers",
    detail: "A recently opened account received money from many unrelated people. This is typical of a money mule account used to collect stolen funds.",
  },
  "MUL-441": {
    title: "Money leaves as fast as it arrives",
    detail: "Almost everything paid into the account was paid straight out again, typical of a mule account passing stolen money on.",
  },
  "MUL-442": {
    title: "Money moved on within ten minutes",
    detail: "Funds stayed in the account for less than ten minutes before being moved.",
  },
  "MUL-443": {
    title: "Cash-out number seen at several institutions",
    detail: "The phone number receiving the money has been reported at more than one institution.",
  },
  "BEN-450": {
    title: "Large first payment to a new recipient",
    detail: "The first payment to a newly added recipient was unusually large.",
  },
  "BEN-451": {
    title: "Many unrelated senders paying one account",
    detail: "Several unrelated customers sent money to the same account, which suggests it collects money from many victims.",
  },
  "ATO-460": {
    title: "New device, then a new recipient",
    detail: "Someone signed in from a new device and then added a new recipient, a common sign that the account has been taken over.",
  },
  "ATO-461": {
    title: "Password or PIN changed at an odd time",
    detail: "Login details were changed during an unusual session, often the first step of an account takeover.",
  },
  "GEN-400": {
    title: "General fraud signal",
    detail: "The institution flagged this activity, but it does not fit a more specific category.",
  },
};

const REASONS: Record<string, Explanation> = {
  "CORRO:destination": {
    title: "Another institution reported the same receiving account",
    detail: "Two institutions flagging the same account independently is strong evidence of fraud.",
  },
  "CORRO:msisdn": {
    title: "Another institution reported the same receiving phone number",
    detail: "Two institutions flagging the same number independently is strong evidence of fraud.",
  },
  "CORRO:device_profile": {
    title: "Another institution saw the same device",
    detail: "The same phone or computer appeared in fraud reports at different institutions.",
  },
  "KB:known_bad": {
    title: "The receiving account is already on the fraud list",
    detail: "It was confirmed as fraudulent in an earlier case.",
  },
  "KB:known_bad_msisdn": {
    title: "The receiving phone number is already on the fraud list",
    detail: "It was confirmed as fraudulent in an earlier case.",
  },
  "KB:suppressed_legitimate": {
    title: "The receiving account is on the trusted list",
    detail: "It is known to be genuine, which lowers the score.",
  },
  "BANK:above_threshold": {
    title: "The reporting institution's own system rated it high risk",
    detail: "Its fraud score passed that institution's alert threshold.",
  },
  "LINK:device_switch": {
    title: "Same receiving account, different device",
    detail: "Another institution reported this account from a different device, which fits a fraudster switching phones to avoid detection.",
  },
};

const REASON_ORDER = ["CODE", "CORRO", "LINK", "KB", "BANK"];

export const STATUS_HELP: Record<string, string> = {
  validated_fraud: "Kifaru confirmed this as fraud and alerted the institution receiving the money.",
  needs_review: "Not enough evidence yet. Kifaru is waiting for another institution to report the same account.",
  not_fraud: "The evidence did not meet the fraud standard, so no alert was sent.",
};

export const ALERT_STATE_HELP: Record<string, string> = {
  sent: "Sent, waiting for the receiving institution to respond",
  acknowledged: "Acknowledged by the receiving institution",
  actioned: "Actioned by the receiving institution",
  disputed: "Disputed by the receiving institution",
};

/** Plain title and explanation for a risk code such as ATO-460. */
export function riskCodeInfo(code: string, fallbackName = ""): Explanation {
  return RISK_CODES[code] ?? {
    title: fallbackName || code,
    detail: fallbackName ? `${fallbackName}.` : "A fraud signal from the central standard.",
  };
}

export interface ReasonExplanation extends Explanation {
  code: string;
}

/** Explains the validator's reason codes, such as CODE:ATO-460 or CORRO:destination, in a sensible reading order. */
export function explainReasons(reasons: string[]): ReasonExplanation[] {
  const rank = (reason: string) => {
    const index = REASON_ORDER.indexOf(reason.split(":")[0]);
    return index === -1 ? REASON_ORDER.length : index;
  };
  return [...new Set(reasons)]
    .filter((reason) => reason.includes(":"))
    .sort((a, b) => rank(a) - rank(b))
    .map((reason) => {
      const [kind, value = ""] = reason.split(":");
      if (kind === "CODE") return { ...riskCodeInfo(value), code: value };
      return { ...(REASONS[reason] ?? { title: reason.replaceAll("_", " "), detail: "" }), code: reason };
    });
}

function isTrue(value: unknown) {
  return value === true || String(value).toLowerCase() === "true";
}

function isFalse(value: unknown) {
  return value === false || String(value).toLowerCase() === "false";
}

function asNumber(value: unknown) {
  if (value === null || value === "" || typeof value === "boolean") return null;
  const number = Number(value);
  return Number.isFinite(number) ? number : null;
}

function plural(count: number, word: string) {
  return `${count} ${word}${count === 1 ? "" : "s"}`;
}

/** Turns the evidence an institution attached to a report into plain sentences, skipping internal fields. */
export function describeEvidence(fields: Record<string, unknown>): string[] {
  const lines: string[] = [];
  for (const [key, value] of Object.entries(fields)) {
    const number = asNumber(value);
    switch (key) {
      case "synthetic_stream":
      case "dataset_basis":
        break;
      case "sentinel_security_alert":
        if (value && typeof value === "object" && typeof (value as Record<string, unknown>).AlertName === "string") {
          const alert = value as Record<string, unknown>;
          const severity = typeof alert.AlertSeverity === "string" ? `, ${alert.AlertSeverity.toLowerCase()} severity` : "";
          lines.push(`Security alert raised: "${alert.AlertName}"${severity}`);
        }
        break;
      case "is_new_device":
        if (isTrue(value)) lines.push("Signed in from a device this customer had never used");
        break;
      case "is_new_beneficiary":
        if (isTrue(value)) lines.push("Paid a recipient added for the first time");
        break;
      case "credential_changed":
        if (isTrue(value)) lines.push("Password or PIN was changed");
        break;
      case "transaction_attempted":
        if (isFalse(value)) lines.push("No money has moved yet");
        break;
      case "ip_country_changed":
        if (isTrue(value)) lines.push("Connected from a different country than usual");
        break;
      case "vpn_proxy_tor":
        if (isTrue(value)) lines.push("Connected through a VPN, proxy or Tor, which hides the real location");
        break;
      case "is_emulator":
        if (isTrue(value)) lines.push("Used a phone emulator");
        break;
      case "is_rooted":
        if (isTrue(value)) lines.push("Used a rooted (tampered) phone");
        break;
      case "known_customer_pattern":
        if (isTrue(value)) lines.push("Matches this customer's usual behaviour");
        break;
      case "sim_swap_age_days":
        if (number !== null) lines.push(number === 0 ? "SIM card was replaced the same day" : `SIM card was replaced ${plural(number, "day")} earlier`);
        break;
      case "beneficiary_age_minutes":
        if (number !== null) lines.push(`Recipient was added ${plural(number, "minute")} before being paid`);
        break;
      case "account_age_days":
        if (number !== null) lines.push(`Receiving account is ${plural(number, "day")} old`);
        break;
      case "distinct_senders_7d":
        if (number !== null) lines.push(`${plural(number, "different sender")} paid into the account in the past week`);
        break;
      case "flow_through_ratio":
        if (number !== null) lines.push(`${Math.round(number * 100)}% of the money received was paid straight out again`);
        break;
      case "dwell_minutes":
        if (number !== null) lines.push(`Money stayed in the account for ${plural(number, "minute")}`);
        break;
      case "device_profile":
        if (typeof value === "string" && value) {
          lines.push(`Device fingerprint (protected): ${value.length > 12 ? `${value.slice(0, 7)}…${value.slice(-4)}` : value}`);
        }
        break;
      default:
        if (value !== null && value !== undefined && value !== "" && typeof value !== "object") {
          lines.push(`${key.replaceAll("_", " ")}: ${String(value)}`);
        }
    }
  }
  return lines;
}
