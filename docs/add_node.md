```
///////////////////////////////////////////////////////////////////////////////
// MAINTENANCE PREP
////////////////////////////////////////////////////////////////////////////////

(1) Maintenance objective:

     Scale the cluster to add 6 new compute nodes at MDP DC2

  (1a) What should we check to confirm the solution is functioning as expected? 

       Nova compute and neutron agents are operational on the nodes to be added

(2) Departments involved: RPC

(3) Owning department: RPC

(4) Amount of time estimated for maintenance: 60 minutes

--------------------------------------------------------------------------------
(5) Maintenance Steps:
--------------------------------------------------------------------------------

5.0 -  Update ticket with notification that maintenance is beginning.

5.1 -  Log in to deployment node

    # ht login 524262 (cdc bastion)
    # FPT_MDP_VNPT
  
5.2 -  Add the new node(s) to the inventory /etc/genestack/inventory/inventory.yaml 

    Add the nodes to the all group:

    all:
      hosts:
         mdp-vnpt-opz-comp001:
             ansible_host: 10.245.0.81
         mdp-vnpt-opz-comp002:
             ansible_host: 10.245.0.82
         mdp-vnpt-opz-comp003:
             ansible_host: 10.245.0.83
         mdp-vnpt-tpz-comp001:
             ansible_host: 10.245.2.81
         mdp-vnpt-tpz-comp002:
             ansible_host: 10.245.2.82
         mdp-vnpt-tpz-comp003:
             ansible_host: 10.245.2.83 

   And also add it into the compute node group

      openstack_compute_nodes:
            hosts:
                mdp-vnpt-opz-comp001: null
                mdp-vnpt-opz-comp002: null
                mdp-vnpt-opz-comp003: null
                mdp-vnpt-tpz-comp001: null
                mdp-vnpt-tpz-comp002: null
                mdp-vnpt-tpz-comp003: null

  And also add it into the kube node group

        kube_node:  # all k8s enabled nodes need to be in this group
          hosts:
            mdp-vnpt-opz-comp001: null
            mdp-vnpt-opz-comp002: null
            mdp-vnpt-opz-comp003: null
            mdp-vnpt-tpz-comp001: null
            mdp-vnpt-tpz-comp002: null
            mdp-vnpt-tpz-comp003: null

5.3 -      Create a list of hosts and use with --limit '@/root/add_hosts.limit' do not add localhost as it will be added where needed

cat  /root/add_hosts.limit
mdp-vnpt-opz-comp001
mdp-vnpt-opz-comp002
mdp-vnpt-opz-comp003
mdp-vnpt-tpz-comp001
mdp-vnpt-tpz-comp002
mdp-vnpt-tpz-comp003
EOT
            
5.4 -  Ensure network config and packages are current & Update OS packages

    # source /opt/genestack/scripts/genestack.rc
    # ansible openstack_compute_nodes -m ansible.builtin.apt -a "update_cache=true upgrade=dist" --limit '@/root/add_hosts.limit'

    - Confirm that the ping command only lists the nodes that should rebooted

    # ansible openstack_compute_nodes -b -m ansible.builtin.ping --limit '@/root/add_hosts.limit'

    # ansible openstack_compute_nodes -b -m ansible.builtin.shell -a 'reboot' --limit '@/root/add_hosts.limit'

5.5 -  Add compute node as k8s node

    # cd /opt/genestack/submodules/kubespray

  Run scale.yml playbook to add the nodes

# ansible-playbook scale.yml -b --limit '@/root/add_hosts.limit'

5.6 -  Prepare host OS on the compute nodes

   - Update hosts configuration

   # cd /opt/genestack/ansible/playbooks
   # ansible-playbook -b host-setup.yml --limit '@/root/add_hosts.limit'

   # cd /opt/openstack-ops/playbooks
   # ( /root/.venvs/openstack-ops/bin/ansible-playbook -b configure-hosts.yml configure-packagemanager.yml --limit 'localhost:@/root/add_hosts.limit' )

5.7 -  Label and Annotate nodes to begin service deployment

    - Taint compute nodes

for h in $(cat /root/add_hosts.limit); do
    kubectl taint nodes $h key1=value1:PreferNoSchedule
done

    - Label Nodes as compute and OVN nodes

    for h in $(cat /root/add_hosts.limit); do
      kubectl label nodes $h openstack-compute-node=enabled
      kubectl label nodes $h openstack-network-node=enabled
    done

    - Configure OVN nodes

    for h in $(cat /root/add_hosts.limit); do
      kubectl annotate nodes $h ovn.openstack.org/int_bridge='br-int'
      kubectl annotate nodes $h ovn.openstack.org/bridges='br-ex'
      kubectl annotate nodes $h ovn.openstack.org/ports='br-ex:bond1' # Please adjust accordingly
      kubectl annotate nodes $h ovn.openstack.org/mappings='physnet1:br-ex' # Please adjust accordingly
      kubectl annotate nodes $h ovn.openstack.org/availability_zones='az1'
    done

5.8 -  Verify expected changes are deployed:

    # kubectl get pods -n openstack -o wide |egrep `paste -s -d '|' /root/add_hosts.limit`

    # kubectl -n openstack exec -it openstack-admin-client -- openstack compute service list;openstack network agent list

5.8 -  Update ticket to confirm end of maintenance

-------------------------------------------------------------------------------
(6) Escalation procedure // *DO NOT ABORT MAINTENANCE UNTIL FOLLOWED*
--------------------------------------------------------------------------------

6.1 -  No escalation procedures

--------------------------------------------------------------------------------
(7) Rollback plan // *REQUIRED*
--------------------------------------------------------------------------------

7.1 -  Disable all nova, neutron services on the failed nodes for further analysis

--------------------------------------------------------------------------------
(8) Post Maintenance Notification 
--------------------------------------------------------------------------------
8.1 Success) Update ticket
8.2 Failure) Update ticket
```