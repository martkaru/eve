// Copyright (c) 2017 Zededa, Inc.
// All rights reserved.

// ACL configlet for overlay and underlay interface towards domU

package main

import (
	"fmt"       
	"log"
	"strconv"
	"github.com/zededa/go-provision/types"
)

// iptablesRule is the list of parmeters after the "-A", "FORWARD"
type IptablesRuleList []IptablesRule
type IptablesRule []string


func createACLConfiglet(ifname string, isMgmt bool, ACLs []types.ACE,
     ipVer int, overlayIP string) {
	fmt.Printf("createACLConfiglet: ifname %s, ACLs %v\n", ifname, ACLs)
	rules := aclToRules(ifname, ACLs, ipVer, overlayIP)
	for _, rule := range rules {
		fmt.Printf("createACLConfiglet: rule %v\n", rule)
		args := rulePrefix("-A", isMgmt, ipVer, rule)
		if args == nil {
			fmt.Printf("createACLConfiglet: skipping rule %v\n",
				rule)
			continue
		}
		args = append(args, rule...)
		if ipVer == 4 {
			iptableCmd(args...)
		} else if ipVer == 6 {
			ip6tableCmd(args...)
		}
	}
	if overlayIP != "" {
		// Manually add rules so that lispers.net doesn't see and drop
		// the packet on dbo1x0
		ip6tableCmd("-A", "FORWARD", "-i", ifname, "-j", "DROP")
	}
}

// Returns a list of iptables commands, witout the initial "-A FORWARD"
func aclToRules(ifname string, ACLs []types.ACE, ipVer int,
     overlayIP string) IptablesRuleList {
	rulesList := IptablesRuleList{}
	if overlayIP != "" {
		// Need to allow local communication */
		// Note that sufficient for src or dst to be local
		rule1 := []string{"-i", ifname, "-m", "set", "--match-set",
			"local.ipv6", "dst", "-j", "ACCEPT"}
		rule2 := []string{"-i", ifname, "-m", "set", "--match-set",
			"local.ipv6", "src", "-j", "ACCEPT"}
		rule3 := []string{"-i", ifname, "-d", overlayIP, "-j", "ACCEPT"}
		rule4 := []string{"-i", ifname, "-s", overlayIP, "-j", "ACCEPT"}
		rulesList = append(rulesList, rule1, rule2, rule3, rule4)
	}
	for _, ace := range ACLs {
		rules := aceToRules(ifname, ace, ipVer)
		rulesList = append(rulesList, rules...)
	}
	// Implicit drop at the end
	outArgs := []string{"-i", ifname, "-j", "DROP"}
	inArgs := []string{"-o", ifname, "-j", "DROP"}
	rulesList = append(rulesList, outArgs, inArgs)
	return rulesList
}

func aceToRules(ifname string, ace types.ACE, ipVer int) IptablesRuleList {
	outArgs := []string{"-i", ifname}
	inArgs := []string{"-o", ifname}
	// XXX should we always add local match on eid if isOverlay?
	// 	outArgs -s eid; inArgs -d eid
	// but don't know eid here. See below.
	for _, match := range ace.Matches {
		addOut := []string{}
		addIn := []string{}
		switch match.Type {
		case "ip":
			addOut = []string{"-d", match.Value}
			addIn = []string{"-s", match.Value}
		case "protocol":
			addOut = []string{"-p", match.Value}
			addIn = []string{"-p", match.Value}
		case "fport":
			addOut = []string{"-m", "--dport", match.Value}
			addIn = []string{"-m", "--sport", match.Value}
		case "lport":
			addOut = []string{"-m", "--sport", match.Value}
			addIn = []string{"-m", "--dport", match.Value}
		case "host":
			// Ensure the sets exists; create if not
			// XXX need to feed it into dnsmasq as well; restart
			// dnsmasq. SIGHUP?
			// XXX want created bool to determine whether to restart
			if err := ipsetCreatePair(match.Value); err != nil {
				log.Println("ipset create for ",
					match.Value, err)
			}

			var ipsetName string
			if ipVer == 4 {
				ipsetName = "ipv4." + match.Value
			} else if ipVer == 6 {
				ipsetName = "ipv6." + match.Value
			}
			addOut = []string{"-m", "set", "--match-set",
				ipsetName, "dst"}
			addIn = []string{"-m", "set", "--match-set",
				ipsetName, "src"}
		case "eidset":
			// The eidset only applies to IPv6 overlay
			// XXX shouldn we also check local EID?
			ipsetName := "eids." + ifname	
			addOut = []string{"-m", "set", "--match-set",
				ipsetName, "dst"}
			addIn = []string{"-m", "set", "--match-set",
				ipsetName, "src"}
		default:
			// XXX add more types; error if unknown.
			log.Println("Unsupported ACE match type: ",
				match.Type)
		}
		outArgs = append(outArgs, addOut...)
		inArgs = append(inArgs, addIn...)
	}
	foundDrop := false
	foundLimit := false
	unlimitedInArgs := inArgs
	unlimitedOutArgs := outArgs
	for _, action := range ace.Actions {
		if action.Drop {
			foundDrop = true
		} else if action.Limit {
			foundLimit = true
			// -m limit --limit 4/s --limit-burst 4
			limit := strconv.Itoa(action.LimitRate) + "/" +
			      action.LimitUnit
			burst := strconv.Itoa(action.LimitBurst)
			add := []string{"-m", "limit", "--limit", limit,
				"--limit-burst", burst}
			outArgs = append(outArgs, add...)
			inArgs = append(inArgs, add...)
		}
	}
	if foundDrop {
		outArgs = append(outArgs, []string{"-j", "DROP"}...)
		inArgs = append(inArgs, []string{"-j", "DROP"}...)
	} else {
		// Default
		outArgs = append(outArgs, []string{"-j", "ACCEPT"}...)
		inArgs = append(inArgs, []string{"-j", "ACCEPT"}...)
	}
	fmt.Printf("outArgs %v\n", outArgs)
	fmt.Printf("inArgs %v\n", inArgs)
	rulesList := IptablesRuleList{}
	rulesList = append(rulesList, outArgs, inArgs)
	if foundLimit {
		// Add separate DROP without the limit to count the excess
		unlimitedOutArgs = append(unlimitedOutArgs,
			[]string{"-j", "DROP"}...)
		unlimitedInArgs = append(unlimitedInArgs,
			[]string{"-j", "DROP"}...)
		fmt.Printf("unlimitedOutArgs %v\n", unlimitedOutArgs)
		fmt.Printf("unlimitedInArgs %v\n", unlimitedInArgs)
		rulesList = append(rulesList, unlimitedOutArgs, unlimitedInArgs)
	}	
	return rulesList
}

// Determine which rules to skip and what prefix/table to use
func rulePrefix(operation string, isMgmt bool, ipVer int,
     rule IptablesRule) IptablesRule {
	prefix := []string{}
	if isMgmt {
		// Enforcing sending on OUTPUT. Enforcing receiving
		// using FORWARD since packet FORWARDED from lispers.net
		// interface.
		// XXX need to rewrite -o to -i and vice versa??
		// Should match on dst for OUTPUT to dbo1x0
		// and src for FORWARD to dbox1x0
		if rule[0] == "-o" {
			prefix = []string{operation, "FORWARD"}
		} else if rule[0] == "-i" {
			prefix = []string{operation, "OUTPUT"}
			rule[0] = "-o"
		} else {
			return nil
		}
	} else if ipVer == 6 {
		// The input rules (from domU are applied to raw to intercept
		// before lisp/pcap can pick them up.
		// The output rules (to domU) are applied in forwarding path
		// since packets are forwarded from lispers.net interface after
		// decap.
		if rule[0] == "-i" {
			prefix = []string{"-t", "raw", operation, "PREROUTING"}
		} else if rule[0] == "-o" {
			prefix = []string{operation, "FORWARD"}
		} else {
			return nil
		}
	} else {
		// Underlay
		prefix = []string{operation, "FORWARD"}
	}
	return prefix
}

func equalRule(r1 IptablesRule, r2 IptablesRule) bool {
	if len(r1) != len(r2) {
		return false
	}
	for i, _ := range r1 {
		if r1[i] != r2[i] {
			return false
		}
	}
	return true
}

func containsRule(set IptablesRuleList, member IptablesRule) bool {
	for _, r := range set {
		if equalRule(r, member) {
			return true
		}
	}
	return false
}

func updateACLConfiglet(ifname string, isMgmt bool, oldACLs []types.ACE,
     newACLs []types.ACE, ipVer int, overlayIP string) {
	fmt.Printf("updateACLConfiglet: ifname %s, oldACLs %v newACLs %v\n",
		ifname, oldACLs, newACLs)
	oldRules := aclToRules(ifname, oldACLs, ipVer, overlayIP)
	newRules := aclToRules(ifname, newACLs, ipVer, overlayIP)
	// Look for old which should be deleted
	for _, rule := range oldRules {
		if containsRule(newRules, rule) {
			continue
		}
		fmt.Printf("modifyACLConfiglet: delete rule %v\n", rule)
		args := rulePrefix("-D", isMgmt, ipVer, rule)
		if args == nil {
			fmt.Printf("modifyACLConfiglet: skipping delete rule %v\n",
				rule)
			continue
		}
		args = append(args, rule...)
		if ipVer == 4 {
			iptableCmd(args...)
		} else if ipVer == 6 {
			ip6tableCmd(args...)
		}
	}
	// Look for new which should be inserted
	for _, rule := range newRules {
		if containsRule(oldRules, rule) {
			continue
		}
		fmt.Printf("modifyACLConfiglet: add rule %v\n", rule)
		args := rulePrefix("-I", isMgmt, ipVer, rule)
		if args == nil {
			fmt.Printf("modifyACLConfiglet: skipping insert rule %v\n",
				rule)
			continue
		}
		args = append(args, rule...)
		if ipVer == 4 {
			iptableCmd(args...)
		} else if ipVer == 6 {
			ip6tableCmd(args...)
		}
	}
}

func deleteACLConfiglet(ifname string, isMgmt bool, ACLs []types.ACE,
     ipVer int, overlayIP string) {
	fmt.Printf("deleteACLConfiglet: ifname %s ACLs %v\n", ifname, ACLs)
	rules := aclToRules(ifname, ACLs, ipVer, overlayIP)
	for _, rule := range rules {
		fmt.Printf("deleteACLConfiglet: rule %v\n", rule)
		args := rulePrefix("-D", isMgmt, ipVer, rule)
		if args == nil {
			fmt.Printf("deleteACLConfiglet: skipping rule %v\n",
				rule)
			continue
		}
		args = append(args, rule...)
		if ipVer == 4 {
			iptableCmd(args...)
		} else if ipVer == 6 {
			ip6tableCmd(args...)
		}
	}
	if overlayIP != "" {
		// Manually delete the manual add above
		ip6tableCmd("-D", "FORWARD", "-i", ifname, "-j", "DROP")
	}
}
